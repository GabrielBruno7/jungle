package app

import "errors"

var (
	ErrNotFound = errors.New("app: not found")

	ErrWalletAlreadyExists = errors.New("app: wallet already exists for this player and currency")

	ErrIdempotencyConflict = errors.New("app: idempotency key reused with a different payload")

	ErrOperationIdentityConflict = errors.New("app: operation already recorded under a different idempotency key")

	ErrConcurrencyConflict = errors.New("app: concurrent modification detected")

	ErrWalletMismatch = errors.New("app: wallet does not belong to the given player")

	ErrForbidden = errors.New("app: not authorized for this provider")
)
