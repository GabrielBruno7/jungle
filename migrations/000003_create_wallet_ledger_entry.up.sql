CREATE TABLE wallet_ledger_entry (
    id             UUID PRIMARY KEY,
    wallet_id      UUID NOT NULL REFERENCES wallet (id),
    transaction_id UUID NOT NULL REFERENCES wager_transaction (id),

    direction TEXT NOT NULL CHECK (direction IN ('DEBIT', 'CREDIT')),

    amount_minor_units         BIGINT NOT NULL CHECK (amount_minor_units > 0),
    balance_before_minor_units BIGINT NOT NULL CHECK (balance_before_minor_units >= 0),
    balance_after_minor_units  BIGINT NOT NULL CHECK (balance_after_minor_units >= 0),

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- At most one ledger entry per (wallet, transaction) — a transaction
    -- moves a wallet's balance at most once.
    CONSTRAINT wallet_ledger_entry_wallet_transaction_unique
        UNIQUE (wallet_id, transaction_id),

    -- The arithmetic invariant itself, enforced by the database regardless
    -- of what application code computed: balanceAfter = balanceBefore ±
    -- amount, depending on direction.
    CONSTRAINT wallet_ledger_entry_balance_math CHECK (
        (direction = 'DEBIT' AND balance_after_minor_units = balance_before_minor_units - amount_minor_units)
        OR
        (direction = 'CREDIT' AND balance_after_minor_units = balance_before_minor_units + amount_minor_units)
    )
);

CREATE INDEX wallet_ledger_entry_wallet_id_created_at_idx
    ON wallet_ledger_entry (wallet_id, created_at, id);

-- Append-only: once written, a ledger entry can never be changed or
-- removed, by anyone or anything, including a raw psql session or an
-- application bug. Corrections are always a new entry, never an edit.
CREATE FUNCTION wallet_ledger_entry_forbid_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger_entry rows are append-only and cannot be updated or deleted';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER wallet_ledger_entry_no_update
    BEFORE UPDATE ON wallet_ledger_entry
    FOR EACH ROW
    EXECUTE FUNCTION wallet_ledger_entry_forbid_mutation();

CREATE TRIGGER wallet_ledger_entry_no_delete
    BEFORE DELETE ON wallet_ledger_entry
    FOR EACH ROW
    EXECUTE FUNCTION wallet_ledger_entry_forbid_mutation();
