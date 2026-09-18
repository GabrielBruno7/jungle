CREATE TABLE wager_transaction (
    id       UUID PRIMARY KEY,
    wallet_id UUID NOT NULL REFERENCES wallet (id),
    player_id UUID NOT NULL,

    kind   TEXT NOT NULL CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    status TEXT NOT NULL CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),

    amount_minor_units BIGINT NOT NULL CHECK (amount_minor_units >= 0),
    currency           CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),

    provider_id             TEXT,
    external_transaction_id TEXT,
    idempotency_key         TEXT,
    payload_hash            TEXT,
    round_id                TEXT,
    game_id                 TEXT,

    reference_external_transaction_id TEXT,
    reference_transaction_id          UUID REFERENCES wager_transaction (id),

    failure_code                   TEXT,
    resulting_balance_minor_units  BIGINT,

    reference_attempts        INT NOT NULL DEFAULT 0,
    reference_first_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    reference_next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT wager_transaction_provider_external_unique
        UNIQUE (provider_id, external_transaction_id),

    CONSTRAINT wager_transaction_idempotency_key_unique
        UNIQUE (idempotency_key),

    CONSTRAINT wager_transaction_origin CHECK (
        (kind = 'OPENING'
            AND provider_id IS NULL AND external_transaction_id IS NULL
            AND idempotency_key IS NULL AND payload_hash IS NULL
            AND round_id IS NULL AND game_id IS NULL)
        OR
        (kind <> 'OPENING'
            AND provider_id IS NOT NULL AND external_transaction_id IS NOT NULL
            AND idempotency_key IS NOT NULL AND payload_hash IS NOT NULL
            AND round_id IS NOT NULL AND game_id IS NOT NULL)
    ),

    CONSTRAINT wager_transaction_reference CHECK (
        (kind IN ('REFUND', 'ROLLBACK') AND reference_external_transaction_id IS NOT NULL)
        OR (kind = 'WIN')
        OR (kind IN ('BET', 'LOSS', 'OPENING') AND reference_external_transaction_id IS NULL)
    ),

    CONSTRAINT wager_transaction_failure_code CHECK (
        (status IN ('REJECTED', 'FAILED') AND failure_code IS NOT NULL)
        OR (status NOT IN ('REJECTED', 'FAILED') AND failure_code IS NULL)
    ),

    CONSTRAINT wager_transaction_resulting_balance CHECK (
        (status = 'PROCESSED' AND resulting_balance_minor_units IS NOT NULL)
        OR (status <> 'PROCESSED' AND resulting_balance_minor_units IS NULL)
    )
);

CREATE UNIQUE INDEX wager_transaction_one_opening_per_wallet
    ON wager_transaction (wallet_id)
    WHERE kind = 'OPENING';

CREATE INDEX wager_transaction_provider_external_idx
    ON wager_transaction (provider_id, external_transaction_id);

CREATE INDEX wager_transaction_pending_reference_idx
    ON wager_transaction (reference_next_attempt_at)
    WHERE status = 'PENDING_REFERENCE';

CREATE FUNCTION wager_transaction_forbid_terminal_update() RETURNS trigger AS $$
BEGIN
    IF OLD.status IN ('PROCESSED', 'REJECTED', 'FAILED') THEN
        RAISE EXCEPTION 'wager_transaction % is in terminal status % and cannot be modified', OLD.id, OLD.status;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER wager_transaction_terminal_guard
    BEFORE UPDATE ON wager_transaction
    FOR EACH ROW
    EXECUTE FUNCTION wager_transaction_forbid_terminal_update();
