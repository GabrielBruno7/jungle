-- Transactional outbox: a row here is written in the same SQL transaction
-- as the domain change it announces, so an event only ever exists once its
-- cause is durably committed. A separate worker (Phase 7) publishes
-- pending rows to SQS/wherever, with retry/backoff and a lightweight
-- lock so multiple publisher instances don't double-publish the same row.
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

    -- Lightweight claim so two publisher instances don't grab the same
    -- unpublished row at once; a lock past its locked_until is stale and
    -- up for grabs again (recovers from a publisher that died mid-work).
    locked_by     TEXT,
    locked_until  TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- What a publisher polls: unpublished rows whose next attempt is due.
CREATE INDEX outbox_pending_idx
    ON outbox (next_attempt_at)
    WHERE published_at IS NULL;
