CREATE TABLE outbox (
    event_id UUID PRIMARY KEY,

    aggregate_id TEXT NOT NULL,
    event_type   TEXT NOT NULL,
    payload      JSONB NOT NULL,

    occurred_at    TIMESTAMPTZ NOT NULL,
    correlation_id UUID NOT NULL,
    causation_id   UUID,

    attempts        INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at    TIMESTAMPTZ,

    locked_by     TEXT,
    locked_until  TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX outbox_pending_idx
    ON outbox (next_attempt_at)
    WHERE published_at IS NULL;
