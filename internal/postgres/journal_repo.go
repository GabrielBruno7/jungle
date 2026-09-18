package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"jungle/internal/domain/journal"
	"jungle/internal/domain/money"
)

type journalRepo struct {
	q Querier
}

func (r *journalRepo) Insert(ctx context.Context, entries ...journal.Entry) error {
	const query = `
		INSERT INTO journal_entry (
			id, transaction_id, account, direction, amount_minor_units, currency, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)`

	for _, entry := range entries {
		_, err := r.q.Exec(ctx, query,
			entry.ID(), entry.TransactionID(), string(entry.Account()), string(entry.Direction()),
			entry.Amount().MinorUnits(), string(entry.Amount().Currency()), entry.CreatedAt(),
		)
		if err != nil {
			return fmt.Errorf("inserting journal entry: %w", err)
		}
	}
	return nil
}

func (r *journalRepo) ByTransaction(ctx context.Context, transactionID uuid.UUID) ([]journal.Entry, error) {
	const query = `
		SELECT id, transaction_id, account, direction, amount_minor_units, currency, created_at
		FROM journal_entry
		WHERE transaction_id = $1
		ORDER BY direction, created_at`

	rows, err := r.q.Query(ctx, query, transactionID)
	if err != nil {
		return nil, fmt.Errorf("selecting journal entries: %w", err)
	}
	defer rows.Close()

	var entries []journal.Entry
	for rows.Next() {
		var (
			id, txID  uuid.UUID
			account   string
			direction string
			amount    int64
			currency  string
			createdAt time.Time
		)

		if err := rows.Scan(&id, &txID, &account, &direction, &amount, &currency, &createdAt); err != nil {
			return nil, fmt.Errorf("scanning journal entry: %w", err)
		}

		cur := money.Currency(currency)
		entry, err := journal.Rehydrate(journal.RehydrateParams{
			ID:            id,
			TransactionID: txID,
			Account:       journal.Account(account),
			Direction:     journal.Direction(direction),
			Amount:        money.FromMinorUnits(amount, cur),
			CreatedAt:     createdAt,
		})
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating journal entries: %w", err)
	}
	return entries, nil
}

func (r *journalRepo) GlobalImbalance(ctx context.Context) (int64, error) {
	const query = `
		SELECT COALESCE(SUM(
			CASE WHEN direction = 'CREDIT' THEN amount_minor_units ELSE -amount_minor_units END
		), 0)
		FROM journal_entry`

	var imbalance int64
	if err := r.q.QueryRow(ctx, query).Scan(&imbalance); err != nil {
		return 0, fmt.Errorf("computing journal imbalance: %w", err)
	}
	return imbalance, nil
}

func (r *journalRepo) AccountBalance(ctx context.Context, account journal.Account) (int64, error) {
	const query = `
		SELECT COALESCE(SUM(
			CASE WHEN direction = 'CREDIT' THEN amount_minor_units ELSE -amount_minor_units END
		), 0)
		FROM journal_entry
		WHERE account = $1`

	var balance int64
	if err := r.q.QueryRow(ctx, query, string(account)).Scan(&balance); err != nil {
		return 0, fmt.Errorf("computing account balance: %w", err)
	}
	return balance, nil
}
