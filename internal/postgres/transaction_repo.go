package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"jungle/internal/app"
	"jungle/internal/domain/money"
	"jungle/internal/domain/wagertx"
)

type transactionRepo struct {
	q Querier
}

const transactionColumns = `
	id, wallet_id, player_id, kind, status,
	amount_minor_units, currency,
	provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
	reference_external_transaction_id, reference_transaction_id,
	failure_code, resulting_balance_minor_units,
	created_at, updated_at`

func (r *transactionRepo) Insert(ctx context.Context, tx wagertx.WagerTransaction) error {
	const query = `
		INSERT INTO wager_transaction (
			id, wallet_id, player_id, kind, status,
			amount_minor_units, currency,
			provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
			reference_external_transaction_id, reference_transaction_id,
			failure_code, resulting_balance_minor_units,
			created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7,
			$8, $9, $10, $11, $12, $13,
			$14, $15,
			$16, $17,
			$18, $19
		)`

	_, err := r.q.Exec(ctx, query,
		tx.ID(), tx.WalletID(), tx.PlayerID(), string(tx.Kind()), string(tx.Status()),
		tx.Money().MinorUnits(), string(tx.Money().Currency()),
		nullString(tx.ProviderID()), nullString(tx.ExternalTransactionID()),
		nullString(tx.IdempotencyKey()), nullString(tx.PayloadHash()),
		nullString(tx.RoundID()), nullString(tx.GameID()),
		nullString(tx.ReferenceExternalTransactionID()), nullUUID(tx.ReferenceTransactionID()),
		nullString(string(tx.FailureCode())), resultingBalanceArg(tx),
		tx.CreatedAt(), tx.UpdatedAt(),
	)
	if err != nil {
		switch name, ok := constraintViolation(err); {
		case ok && name == "wager_transaction_idempotency_key_unique":
			return app.ErrIdempotencyConflict
		case ok && name == "wager_transaction_provider_external_unique":
			return app.ErrOperationIdentityConflict
		case ok && name == "wager_transaction_one_opening_per_wallet":
			return app.ErrWalletAlreadyExists
		}
		return fmt.Errorf("inserting wager transaction: %w", err)
	}
	return nil
}

func (r *transactionRepo) Update(ctx context.Context, tx wagertx.WagerTransaction) error {
	const query = `
		UPDATE wager_transaction
		SET status = $1,
		    reference_transaction_id = $2,
		    failure_code = $3,
		    resulting_balance_minor_units = $4,
		    updated_at = $5
		WHERE id = $6`

	tag, err := r.q.Exec(ctx, query,
		string(tx.Status()), nullUUID(tx.ReferenceTransactionID()),
		nullString(string(tx.FailureCode())), resultingBalanceArg(tx),
		tx.UpdatedAt(), tx.ID(),
	)
	if err != nil {
		return fmt.Errorf("updating wager transaction: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return app.ErrNotFound
	}
	return nil
}

func (r *transactionRepo) GetByID(ctx context.Context, id uuid.UUID) (wagertx.WagerTransaction, error) {
	const query = `SELECT ` + transactionColumns + ` FROM wager_transaction WHERE id = $1`
	return r.scanOne(ctx, query, id)
}

func (r *transactionRepo) GetByIdempotencyKey(ctx context.Context, key string) (wagertx.WagerTransaction, error) {
	const query = `SELECT ` + transactionColumns + ` FROM wager_transaction WHERE idempotency_key = $1`
	return r.scanOne(ctx, query, key)
}

func (r *transactionRepo) GetByProviderExternalID(ctx context.Context, providerID, externalID string) (wagertx.WagerTransaction, error) {
	const query = `
		SELECT ` + transactionColumns + `
		FROM wager_transaction
		WHERE provider_id = $1 AND external_transaction_id = $2`
	return r.scanOne(ctx, query, providerID, externalID)
}

func (r *transactionRepo) HasSuccessfulReversal(ctx context.Context, referenceID uuid.UUID) (bool, error) {
	const query = `
		SELECT EXISTS (
			SELECT 1 FROM wager_transaction
			WHERE reference_transaction_id = $1
			  AND kind IN ('REFUND', 'ROLLBACK')
			  AND status = 'PROCESSED'
		)`

	var exists bool
	if err := r.q.QueryRow(ctx, query, referenceID).Scan(&exists); err != nil {
		return false, fmt.Errorf("checking existing reversal: %w", err)
	}
	return exists, nil
}

func (r *transactionRepo) ClaimPendingReference(ctx context.Context, limit int, now time.Time) ([]app.PendingReferenceRecord, error) {
	const query = `
		SELECT ` + transactionColumns + `, reference_attempts, reference_first_seen_at
		FROM wager_transaction
		WHERE status = 'PENDING_REFERENCE'
		  AND reference_next_attempt_at <= $1
		ORDER BY reference_next_attempt_at
		LIMIT $2
		FOR UPDATE SKIP LOCKED`

	rows, err := r.q.Query(ctx, query, now, limit)
	if err != nil {
		return nil, fmt.Errorf("claiming pending references: %w", err)
	}
	defer rows.Close()

	var records []app.PendingReferenceRecord
	for rows.Next() {
		var (
			raw         transactionRow
			attempts    int
			firstSeenAt time.Time
		)
		if err := raw.scan(rows, &attempts, &firstSeenAt); err != nil {
			return nil, err
		}
		tx, err := raw.toDomain()
		if err != nil {
			return nil, err
		}
		records = append(records, app.PendingReferenceRecord{
			Transaction: tx,
			Attempts:    attempts,
			FirstSeenAt: firstSeenAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating pending references: %w", err)
	}
	return records, nil
}

func (r *transactionRepo) RecordReferenceAttempt(ctx context.Context, id uuid.UUID, attempts int, nextAttemptAt time.Time) error {
	const query = `
		UPDATE wager_transaction
		SET reference_attempts = $1, reference_next_attempt_at = $2
		WHERE id = $3 AND status = 'PENDING_REFERENCE'`

	if _, err := r.q.Exec(ctx, query, attempts, nextAttemptAt, id); err != nil {
		return fmt.Errorf("recording reference attempt: %w", err)
	}
	return nil
}

func (r *transactionRepo) CountPendingReference(ctx context.Context) (int, error) {
	const query = `SELECT count(*) FROM wager_transaction WHERE status = 'PENDING_REFERENCE'`

	var count int
	if err := r.q.QueryRow(ctx, query).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting pending references: %w", err)
	}
	return count, nil
}

func (r *transactionRepo) scanOne(ctx context.Context, query string, args ...any) (wagertx.WagerTransaction, error) {
	var raw transactionRow
	err := raw.scanRow(r.q.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return wagertx.WagerTransaction{}, app.ErrNotFound
	}
	if err != nil {
		return wagertx.WagerTransaction{}, fmt.Errorf("selecting wager transaction: %w", err)
	}
	return raw.toDomain()
}

type transactionRow struct {
	id               uuid.UUID
	walletID         uuid.UUID
	playerID         uuid.UUID
	kind             string
	status           string
	amountMinorUnits int64
	currency         string

	providerID            *string
	externalTransactionID *string
	idempotencyKey        *string
	payloadHash           *string
	roundID               *string
	gameID                *string

	referenceExternalTransactionID *string
	referenceTransactionID         *uuid.UUID

	failureCode                *string
	resultingBalanceMinorUnits *int64

	createdAt time.Time
	updatedAt time.Time
}

func (t *transactionRow) targets() []any {
	return []any{
		&t.id, &t.walletID, &t.playerID, &t.kind, &t.status,
		&t.amountMinorUnits, &t.currency,
		&t.providerID, &t.externalTransactionID, &t.idempotencyKey, &t.payloadHash, &t.roundID, &t.gameID,
		&t.referenceExternalTransactionID, &t.referenceTransactionID,
		&t.failureCode, &t.resultingBalanceMinorUnits,
		&t.createdAt, &t.updatedAt,
	}
}

func (t *transactionRow) scanRow(row pgx.Row) error {
	return row.Scan(t.targets()...)
}

func (t *transactionRow) scan(rows pgx.Rows, extra ...any) error {
	return rows.Scan(append(t.targets(), extra...)...)
}

func (t *transactionRow) toDomain() (wagertx.WagerTransaction, error) {
	cur := money.Currency(t.currency)

	var resulting money.Money
	if t.resultingBalanceMinorUnits != nil {
		resulting = money.FromMinorUnits(*t.resultingBalanceMinorUnits, cur)
	}

	var referenceID uuid.UUID
	if t.referenceTransactionID != nil {
		referenceID = *t.referenceTransactionID
	}

	return wagertx.Rehydrate(wagertx.RehydrateParams{
		ID:                             t.id,
		WalletID:                       t.walletID,
		PlayerID:                       t.playerID,
		Kind:                           wagertx.Kind(t.kind),
		Money:                          money.FromMinorUnits(t.amountMinorUnits, cur),
		Status:                         wagertx.Status(t.status),
		ProviderID:                     derefString(t.providerID),
		ExternalTransactionID:          derefString(t.externalTransactionID),
		IdempotencyKey:                 derefString(t.idempotencyKey),
		PayloadHash:                    derefString(t.payloadHash),
		RoundID:                        derefString(t.roundID),
		GameID:                         derefString(t.gameID),
		ReferenceExternalTransactionID: derefString(t.referenceExternalTransactionID),
		ReferenceTransactionID:         referenceID,
		FailureCode:                    wagertx.FailureCode(derefString(t.failureCode)),
		ResultingBalance:               resulting,
		CreatedAt:                      t.createdAt,
		UpdatedAt:                      t.updatedAt,
	})
}

func nullUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func resultingBalanceArg(tx wagertx.WagerTransaction) *int64 {
	balance, ok := tx.ResultingBalance()
	if !ok {
		return nil
	}
	minor := balance.MinorUnits()
	return &minor
}
