DROP TRIGGER IF EXISTS wager_transaction_terminal_guard ON wager_transaction;
DROP FUNCTION IF EXISTS wager_transaction_forbid_terminal_update();
DROP TABLE IF EXISTS wager_transaction;
