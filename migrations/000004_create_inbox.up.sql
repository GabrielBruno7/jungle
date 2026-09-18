CREATE TABLE inbox (
    consumer_name TEXT NOT NULL,
    message_id    TEXT NOT NULL,

    payload_hash         TEXT NOT NULL,
    wager_transaction_id UUID NOT NULL REFERENCES wager_transaction (id),

    received_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (consumer_name, message_id)
);
