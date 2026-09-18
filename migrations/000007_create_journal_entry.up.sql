CREATE TABLE journal_entry (
    id             UUID PRIMARY KEY,
    transaction_id UUID NOT NULL REFERENCES wager_transaction (id),

    account   TEXT NOT NULL,
    direction TEXT NOT NULL CHECK (direction IN ('DEBIT', 'CREDIT')),

    amount_minor_units BIGINT NOT NULL CHECK (amount_minor_units > 0),
    currency           CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),

    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX journal_entry_transaction_idx ON journal_entry (transaction_id);
CREATE INDEX journal_entry_account_idx ON journal_entry (account, created_at);

CREATE FUNCTION journal_entry_forbid_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'journal_entry rows are append-only and cannot be updated or deleted';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER journal_entry_no_update
    BEFORE UPDATE ON journal_entry
    FOR EACH ROW
    EXECUTE FUNCTION journal_entry_forbid_mutation();

CREATE TRIGGER journal_entry_no_delete
    BEFORE DELETE ON journal_entry
    FOR EACH ROW
    EXECUTE FUNCTION journal_entry_forbid_mutation();

CREATE FUNCTION journal_entry_must_balance() RETURNS trigger AS $$
DECLARE
    imbalance BIGINT;
BEGIN
    SELECT COALESCE(SUM(
        CASE WHEN direction = 'CREDIT'
             THEN amount_minor_units
             ELSE -amount_minor_units
        END), 0)
    INTO imbalance
    FROM journal_entry
    WHERE transaction_id = NEW.transaction_id;

    IF imbalance <> 0 THEN
        RAISE EXCEPTION
            'journal entries for transaction % do not balance: off by % minor units',
            NEW.transaction_id, imbalance;
    END IF;

    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER journal_entry_balanced
    AFTER INSERT ON journal_entry
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW
    EXECUTE FUNCTION journal_entry_must_balance();
