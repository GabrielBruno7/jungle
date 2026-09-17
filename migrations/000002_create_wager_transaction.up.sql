CREATE TABLE wager_transaction (
    id       UUID PRIMARY KEY,
    wallet_id UUID NOT NULL REFERENCES wallet (id),
    player_id UUID NOT NULL,

    kind   TEXT NOT NULL CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    status TEXT NOT NULL CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),

    amount_minor_units BIGINT NOT NULL CHECK (amount_minor_units >= 0),
    currency           CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),

    -- Present only for external operations (BET/WIN/LOSS/REFUND/ROLLBACK);
    -- NULL for the internal OPENING credit. See wager_transaction_origin
    -- below for the constraint that ties this to `kind`.
    provider_id             TEXT,
    external_transaction_id TEXT,
    idempotency_key         TEXT,
    payload_hash            TEXT,
    round_id                TEXT,
    game_id                 TEXT,

    -- Only meaningful for REFUND/ROLLBACK (required) and WIN (optional).
    reference_external_transaction_id TEXT,
    reference_transaction_id          UUID REFERENCES wager_transaction (id),

    -- Only populated once terminal.
    failure_code                   TEXT,
    resulting_balance_minor_units  BIGINT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- The true business identity of an external operation: it can never be
    -- reprocessed under a different Idempotency-Key.
    CONSTRAINT wager_transaction_provider_external_unique
        UNIQUE (provider_id, external_transaction_id),

    -- The literal idempotency mechanism: replaying the same key returns the
    -- same stored result instead of creating a second row.
    CONSTRAINT wager_transaction_idempotency_key_unique
        UNIQUE (idempotency_key),

    -- OPENING never carries external metadata; every external kind always
    -- does. This is how the schema tells internal and external operations
    -- apart.
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

    -- REFUND/ROLLBACK must carry a reference; BET/LOSS/OPENING never do;
    -- WIN may or may not.
    CONSTRAINT wager_transaction_reference CHECK (
        (kind IN ('REFUND', 'ROLLBACK') AND reference_external_transaction_id IS NOT NULL)
        OR (kind = 'WIN')
        OR (kind IN ('BET', 'LOSS', 'OPENING') AND reference_external_transaction_id IS NULL)
    ),

    -- failure_code is set if and only if the transaction ended REJECTED or
    -- FAILED — never on a success, never missing on a failure.
    CONSTRAINT wager_transaction_failure_code CHECK (
        (status IN ('REJECTED', 'FAILED') AND failure_code IS NOT NULL)
        OR (status NOT IN ('REJECTED', 'FAILED') AND failure_code IS NULL)
    ),

    -- resulting_balance is set if and only if the transaction is PROCESSED
    -- — the snapshot a replay returns.
    CONSTRAINT wager_transaction_resulting_balance CHECK (
        (status = 'PROCESSED' AND resulting_balance_minor_units IS NOT NULL)
        OR (status <> 'PROCESSED' AND resulting_balance_minor_units IS NULL)
    )
);

-- A wallet can only ever have one OPENING transaction — this is what
-- "impedir crédito inicial duplicado" means at the schema level. A partial
-- unique index (only over kind = 'OPENING' rows) rather than a table-wide
-- constraint, since every other kind can repeat per wallet.
CREATE UNIQUE INDEX wager_transaction_one_opening_per_wallet
    ON wager_transaction (wallet_id)
    WHERE kind = 'OPENING';

-- Lookups the HTTP contract requires directly (GET by id is free via the
-- primary key; these cover the other two read endpoints).
CREATE INDEX wager_transaction_provider_external_idx
    ON wager_transaction (provider_id, external_transaction_id);

-- A terminal transaction (PROCESSED/REJECTED/FAILED) must never transition
-- again — enforced in Go by the state machine, and here too, so no bug or
-- stray manual UPDATE can silently reprocess a finished operation.
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
