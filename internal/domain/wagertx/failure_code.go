package wagertx

// FailureCode is a stable, documented reason a WagerTransaction ended in
// REJECTED or FAILED. Every rejection/failure must carry one — never a
// bare "it didn't work".
type FailureCode string

const (
	// FailureInsufficientBalance: a BET couldn't be covered by the
	// wallet's balance.
	FailureInsufficientBalance FailureCode = "INSUFFICIENT_BALANCE"

	// FailureInsufficientBalanceForReversal: a REFUND/ROLLBACK would need
	// to debit more than the wallet currently holds. Deliberately distinct
	// from FailureInsufficientBalance — the challenge requires these two
	// situations to be told apart.
	FailureInsufficientBalanceForReversal FailureCode = "INSUFFICIENT_BALANCE_FOR_REVERSAL"

	// FailureReferenceNotFound: a REFUND/ROLLBACK's reference never
	// arrived before the retry budget (max attempts or TTL) was
	// exhausted.
	FailureReferenceNotFound FailureCode = "REFERENCE_NOT_FOUND"

	// FailureReferenceMismatch: the reference was found, but disagrees
	// with this operation on provider, player, wallet, currency, or round.
	FailureReferenceMismatch FailureCode = "REFERENCE_MISMATCH"

	// FailureReferenceNotSuccessful: the reference was found and resolved,
	// but it never completed successfully (REJECTED or FAILED itself), so
	// there is nothing valid left to reverse.
	FailureReferenceNotSuccessful FailureCode = "REFERENCE_NOT_SUCCESSFUL"

	// FailureDuplicateReversal: the referenced transaction already has a
	// successful reversal (REFUND or ROLLBACK) — a second one, of either
	// kind, must not be allowed to succeed.
	FailureDuplicateReversal FailureCode = "DUPLICATE_REVERSAL"

	// FailureCurrencyMismatch: the operation's currency does not match
	// the wallet's.
	FailureCurrencyMismatch FailureCode = "CURRENCY_MISMATCH"

	// FailureInvalidAmount: the amount is not valid for this kind (e.g. a
	// LOSS with a non-zero amount).
	FailureInvalidAmount FailureCode = "INVALID_AMOUNT"
)
