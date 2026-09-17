DROP TRIGGER IF EXISTS wallet_ledger_entry_no_delete ON wallet_ledger_entry;
DROP TRIGGER IF EXISTS wallet_ledger_entry_no_update ON wallet_ledger_entry;
DROP FUNCTION IF EXISTS wallet_ledger_entry_forbid_mutation();
DROP TABLE IF EXISTS wallet_ledger_entry;
