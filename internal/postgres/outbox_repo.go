package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"jungle/internal/app"
	"jungle/internal/domain/event"
)

type outboxRepo struct {
	q Querier
}

func (r *outboxRepo) Insert(ctx context.Context, events ...event.Envelope) error {
	const query = `
		INSERT INTO outbox (
			event_id, aggregate_id, event_type, payload,
			occurred_at, correlation_id, causation_id,
			attempts, next_attempt_at, created_at, trace_parent
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 0, $8, $8, $9)`

	for _, evt := range events {
		payload, err := json.Marshal(evt)
		if err != nil {
			return fmt.Errorf("marshaling event %s: %w", evt.EventType, err)
		}

		var causation *uuid.UUID
		if evt.CausationID != nil {
			causation = evt.CausationID
		}

		_, err = r.q.Exec(ctx, query,
			evt.EventID, evt.AggregateID, string(evt.EventType), payload,
			evt.OccurredAt, evt.CorrelationID, causation,
			evt.OccurredAt, nullString(currentTraceParent(ctx)),
		)
		if err != nil {
			return fmt.Errorf("inserting outbox event: %w", err)
		}
	}
	return nil
}

func (r *outboxRepo) Claim(ctx context.Context, owner string, limit int, lockFor time.Duration, now time.Time) ([]app.OutboxRecord, error) {
	const query = `
		WITH claimed AS (
			SELECT event_id
			FROM outbox
			WHERE published_at IS NULL
			  AND next_attempt_at <= $1
			  AND (locked_until IS NULL OR locked_until < $1)
			ORDER BY next_attempt_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE outbox o
		SET locked_by = $3, locked_until = $4
		FROM claimed
		WHERE o.event_id = claimed.event_id
		RETURNING o.event_id, o.aggregate_id, o.event_type, o.payload, o.attempts, o.trace_parent`

	rows, err := r.q.Query(ctx, query, now, limit, owner, now.Add(lockFor))
	if err != nil {
		return nil, fmt.Errorf("claiming outbox events: %w", err)
	}
	defer rows.Close()

	var records []app.OutboxRecord
	for rows.Next() {
		var rec app.OutboxRecord
		var traceParent *string
		if err := rows.Scan(&rec.EventID, &rec.AggregateID, &rec.Type, &rec.Payload, &rec.Attempts, &traceParent); err != nil {
			return nil, fmt.Errorf("scanning outbox event: %w", err)
		}
		rec.TraceParent = derefString(traceParent)
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating outbox events: %w", err)
	}
	return records, nil
}

func (r *outboxRepo) MarkPublished(ctx context.Context, eventID uuid.UUID, now time.Time) error {
	const query = `
		UPDATE outbox
		SET published_at = $1, locked_by = NULL, locked_until = NULL
		WHERE event_id = $2 AND published_at IS NULL`

	if _, err := r.q.Exec(ctx, query, now, eventID); err != nil {
		return fmt.Errorf("marking outbox event published: %w", err)
	}
	return nil
}

func (r *outboxRepo) OldestPending(ctx context.Context, now time.Time) (time.Duration, bool, error) {
	const query = `SELECT min(occurred_at) FROM outbox WHERE published_at IS NULL`

	var oldest *time.Time
	if err := r.q.QueryRow(ctx, query).Scan(&oldest); err != nil {
		return 0, false, fmt.Errorf("measuring outbox lag: %w", err)
	}
	if oldest == nil {
		return 0, false, nil
	}
	return now.Sub(*oldest), true, nil
}

func (r *outboxRepo) Reschedule(ctx context.Context, eventID uuid.UUID, attempts int, nextAttemptAt time.Time) error {
	const query = `
		UPDATE outbox
		SET attempts = $1, next_attempt_at = $2, locked_by = NULL, locked_until = NULL
		WHERE event_id = $3 AND published_at IS NULL`

	if _, err := r.q.Exec(ctx, query, attempts, nextAttemptAt, eventID); err != nil {
		return fmt.Errorf("rescheduling outbox event: %w", err)
	}
	return nil
}

func currentTraceParent(ctx context.Context) string {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ""
	}
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return carrier.Get("traceparent")
}
