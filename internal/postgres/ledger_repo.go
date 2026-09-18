package postgres

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"jungle/internal/app"
	"jungle/internal/domain/money"
	"jungle/internal/domain/wallet"
)

type ledgerRepo struct {
	q Querier
}

func (r *ledgerRepo) Insert(ctx context.Context, entry wallet.WalletLedgerEntry) error {
	const query = `
		INSERT INTO wallet_ledger_entry (
			id, wallet_id, transaction_id, direction,
			amount_minor_units, balance_before_minor_units, balance_after_minor_units,
			created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	_, err := r.q.Exec(ctx, query,
		entry.ID(), entry.WalletID(), entry.TransactionID(), string(entry.Direction()),
		entry.Amount().MinorUnits(), entry.BalanceBefore().MinorUnits(), entry.BalanceAfter().MinorUnits(),
		entry.CreatedAt(),
	)
	if err != nil {
		return fmt.Errorf("inserting ledger entry: %w", err)
	}
	return nil
}

func (r *ledgerRepo) Page(ctx context.Context, walletID uuid.UUID, cursor string, limit int) ([]wallet.WalletLedgerEntry, string, error) {
	var (
		rows interface {
			Next() bool
			Scan(...any) error
			Close()
			Err() error
		}
		err error
	)

	if cursor == "" {
		const query = `
			SELECT id, wallet_id, transaction_id, direction,
			       amount_minor_units, balance_before_minor_units, balance_after_minor_units,
			       created_at, (SELECT currency FROM wallet WHERE id = $1)
			FROM wallet_ledger_entry
			WHERE wallet_id = $1
			ORDER BY created_at, id
			LIMIT $2`
		rows, err = r.q.Query(ctx, query, walletID, limit+1)
	} else {
		after, afterID, decodeErr := decodeLedgerCursor(cursor)
		if decodeErr != nil {
			return nil, "", decodeErr
		}
		const query = `
			SELECT id, wallet_id, transaction_id, direction,
			       amount_minor_units, balance_before_minor_units, balance_after_minor_units,
			       created_at, (SELECT currency FROM wallet WHERE id = $1)
			FROM wallet_ledger_entry
			WHERE wallet_id = $1 AND (created_at, id) > ($2, $3)
			ORDER BY created_at, id
			LIMIT $4`
		rows, err = r.q.Query(ctx, query, walletID, after, afterID, limit+1)
	}
	if err != nil {
		return nil, "", fmt.Errorf("selecting ledger page: %w", err)
	}
	defer rows.Close()

	var entries []wallet.WalletLedgerEntry

	for rows.Next() {
		var (
			id, wID, txID uuid.UUID
			direction     string
			amount        int64
			before        int64
			after         int64
			createdAt     time.Time
			currency      string
		)
		if err := rows.Scan(&id, &wID, &txID, &direction, &amount, &before, &after, &createdAt, &currency); err != nil {
			return nil, "", fmt.Errorf("scanning ledger entry: %w", err)
		}

		cur := money.Currency(currency)
		entry, err := wallet.RehydrateWalletLedgerEntry(wallet.RehydrateWalletLedgerEntryParams{
			ID:            id,
			WalletID:      wID,
			TransactionID: txID,
			Direction:     wallet.LedgerDirection(direction),
			Amount:        money.FromMinorUnits(amount, cur),
			BalanceBefore: money.FromMinorUnits(before, cur),
			BalanceAfter:  money.FromMinorUnits(after, cur),
			CreatedAt:     createdAt,
		})
		if err != nil {
			return nil, "", err
		}

		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterating ledger page: %w", err)
	}

	next := ""
	if len(entries) > limit {
		entries = entries[:limit]
		last := entries[len(entries)-1]
		next = encodeLedgerCursor(last.CreatedAt(), last.ID())
	}

	return entries, next, nil
}

func (r *ledgerRepo) Totals(ctx context.Context, walletID uuid.UUID) (int64, int64, int, error) {
	const query = `
		SELECT
			COALESCE(SUM(amount_minor_units) FILTER (WHERE direction = 'CREDIT'), 0),
			COALESCE(SUM(amount_minor_units) FILTER (WHERE direction = 'DEBIT'), 0),
			COUNT(*)
		FROM wallet_ledger_entry
		WHERE wallet_id = $1`

	var credits, debits int64
	var entries int
	if err := r.q.QueryRow(ctx, query, walletID).Scan(&credits, &debits, &entries); err != nil {
		return 0, 0, 0, fmt.Errorf("summing ledger: %w", err)
	}
	return credits, debits, entries, nil
}

func encodeLedgerCursor(createdAt time.Time, id uuid.UUID) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeLedgerCursor(cursor string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("%w: malformed cursor", app.ErrNotFound)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, uuid.Nil, fmt.Errorf("%w: malformed cursor", app.ErrNotFound)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("%w: malformed cursor timestamp", app.ErrNotFound)
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("%w: malformed cursor id", app.ErrNotFound)
	}
	return createdAt, id, nil
}
