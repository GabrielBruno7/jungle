package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"jungle/internal/app"
)

type inboxRepo struct {
	q Querier
}

func (r *inboxRepo) Get(ctx context.Context, consumerName, messageID string) (app.InboxRecord, bool, error) {
	const query = `
		SELECT consumer_name, message_id, payload_hash, wager_transaction_id, received_at, completed_at
		FROM inbox
		WHERE consumer_name = $1 AND message_id = $2`

	var rec app.InboxRecord
	err := r.q.QueryRow(ctx, query, consumerName, messageID).Scan(
		&rec.ConsumerName, &rec.MessageID, &rec.PayloadHash,
		&rec.WagerTransactionID, &rec.ReceivedAt, &rec.CompletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.InboxRecord{}, false, nil
	}
	if err != nil {
		return app.InboxRecord{}, false, fmt.Errorf("selecting inbox record: %w", err)
	}
	return rec, true, nil
}

func (r *inboxRepo) Insert(ctx context.Context, rec app.InboxRecord) error {
	const query = `
		INSERT INTO inbox (consumer_name, message_id, payload_hash, wager_transaction_id, received_at, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6)`

	_, err := r.q.Exec(ctx, query,
		rec.ConsumerName, rec.MessageID, rec.PayloadHash,
		rec.WagerTransactionID, rec.ReceivedAt, rec.CompletedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting inbox record: %w", err)
	}
	return nil
}
