package wagertx

type FailureCode string

const (
	FailureInsufficientBalance FailureCode = "INSUFFICIENT_BALANCE"

	FailureInsufficientBalanceForReversal FailureCode = "INSUFFICIENT_BALANCE_FOR_REVERSAL"

	FailureReferenceNotFound FailureCode = "REFERENCE_NOT_FOUND"

	FailureReferenceMismatch FailureCode = "REFERENCE_MISMATCH"

	FailureReferenceNotSuccessful FailureCode = "REFERENCE_NOT_SUCCESSFUL"

	FailureDuplicateReversal FailureCode = "DUPLICATE_REVERSAL"

	FailureCurrencyMismatch FailureCode = "CURRENCY_MISMATCH"

	FailureInvalidAmount FailureCode = "INVALID_AMOUNT"

	FailureInfrastructure FailureCode = "INFRASTRUCTURE_FAILURE"
)
