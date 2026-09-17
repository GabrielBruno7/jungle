CREATE TABLE wallet (
    id                  UUID PRIMARY KEY,
    player_id           UUID NOT NULL,
    currency            CHAR(3) NOT NULL,
    balance_minor_units BIGINT NOT NULL,
    version             BIGINT NOT NULL DEFAULT 1,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- One wallet per player per currency (domain rule 6.2).
    CONSTRAINT wallet_player_currency_unique UNIQUE (player_id, currency),

    -- A wallet can never go negative, no matter what application code does.
    CONSTRAINT wallet_balance_non_negative CHECK (balance_minor_units >= 0),

    -- Version starts at 1 and only ever increases; enforced again at the
    -- application layer as the optimistic-concurrency token.
    CONSTRAINT wallet_version_positive CHECK (version >= 1),

    CONSTRAINT wallet_currency_format CHECK (currency ~ '^[A-Z]{3}$')
);
