package httpapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"jungle/internal/app"
	"jungle/internal/domain/money"
	"jungle/internal/domain/wagertx"
)

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

const (
	CodeInvalidRequest      = "INVALID_REQUEST"
	CodeUnauthenticated     = "UNAUTHENTICATED"
	CodeForbidden           = "FORBIDDEN"
	CodeNotFound            = "NOT_FOUND"
	CodeWalletExists        = "WALLET_EXISTS"
	CodeIdempotencyConflict = "IDEMPOTENCY_CONFLICT"
	CodeOperationConflict   = "OPERATION_CONFLICT"
	CodeUnavailable         = "UNAVAILABLE"
)

func abortWith(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, ErrorBody{Code: code, Message: message})
}

func attach(c *gin.Context, err error) {
	if err != nil {
		_ = c.Error(err)
	}
}

func respondError(c *gin.Context, err error) {
	attach(c, err)

	switch {
	case errors.Is(err, app.ErrNotFound):
		abortWith(c, http.StatusNotFound, CodeNotFound, "resource not found")

	case errors.Is(err, app.ErrForbidden):
		abortWith(c, http.StatusForbidden, CodeForbidden, "not authorized for this operation")

	case errors.Is(err, app.ErrWalletAlreadyExists):
		abortWith(c, http.StatusConflict, CodeWalletExists, "a wallet already exists for this player and currency")

	case errors.Is(err, app.ErrIdempotencyConflict):
		abortWith(c, http.StatusConflict, CodeIdempotencyConflict, "idempotency key reused with a different payload")

	case errors.Is(err, app.ErrOperationIdentityConflict):
		abortWith(c, http.StatusConflict, CodeOperationConflict, "operation already recorded under a different idempotency key")

	case errors.Is(err, app.ErrWalletMismatch):
		abortWith(c, http.StatusBadRequest, CodeInvalidRequest, "wallet does not belong to the given player")

	case errors.Is(err, app.ErrConcurrencyConflict):
		abortWith(c, http.StatusServiceUnavailable, CodeUnavailable, "concurrent modification, retry the request")

	case errors.Is(err, money.ErrEmptyAmount),
		errors.Is(err, money.ErrInvalidFormat),
		errors.Is(err, money.ErrNegativeAmount),
		errors.Is(err, money.ErrInvalidCurrency),
		errors.Is(err, money.ErrOverflow),
		errors.Is(err, money.ErrCurrencyMismatch),
		errors.Is(err, wagertx.ErrInvalidKind),
		errors.Is(err, wagertx.ErrInvalidTransaction),
		errors.Is(err, wagertx.ErrOpeningNotExternal),
		errors.Is(err, wagertx.ErrReferenceRequired),
		errors.Is(err, wagertx.ErrReferenceNotApplicable),
		errors.Is(err, wagertx.ErrInvalidAmountForKind):
		abortWith(c, http.StatusBadRequest, CodeInvalidRequest, err.Error())

	default:
		abortWith(c, http.StatusServiceUnavailable, CodeUnavailable, "temporarily unable to process the request")
	}
}
