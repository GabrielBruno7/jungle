DROP TRIGGER IF EXISTS journal_entry_balanced ON journal_entry;
DROP FUNCTION IF EXISTS journal_entry_must_balance();
DROP TRIGGER IF EXISTS journal_entry_no_delete ON journal_entry;
DROP TRIGGER IF EXISTS journal_entry_no_update ON journal_entry;
DROP FUNCTION IF EXISTS journal_entry_forbid_mutation();
DROP TABLE IF EXISTS journal_entry;
