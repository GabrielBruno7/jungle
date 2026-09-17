-- One row per message a consumer has durably handled. The row is written
-- in the same SQL transaction as the domain changes it caused, so
-- received_at and completed_at are always set together today — the two
-- columns exist separately because they mean different things (when we
-- first saw it vs. when handling it was durably confirmed), leaving room
-- for a future claim-before-processing step without a schema change.
CREATE TABLE inbox (
    consumer_name TEXT NOT NULL,
    message_id    TEXT NOT NULL,

    payload_hash         TEXT NOT NULL,
    wager_transaction_id UUID NOT NULL REFERENCES wager_transaction (id),

    received_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- The literal dedup key: the same consumer seeing the same messageId
    -- again (at-least-once redelivery) finds this row and skips
    -- reprocessing entirely.
    PRIMARY KEY (consumer_name, message_id)
);
