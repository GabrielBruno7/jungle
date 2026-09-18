package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"jungle/internal/app"
	"jungle/internal/domain/wagertx"
)

const (
	headerIdempotencyKey  = "Idempotency-Key"
	headerCorrelationID   = "X-Correlation-Id"
	correlationContextKey = "jungle.correlationId"
)

type Handlers struct {
	openWallet   *app.OpenWallet
	processWager *app.ProcessWager
	queries      *app.Queries
	logger       *zap.Logger
}

func NewHandlers(openWallet *app.OpenWallet, processWager *app.ProcessWager, queries *app.Queries, logger *zap.Logger) *Handlers {
	return &Handlers{
		openWallet:   openWallet,
		processWager: processWager,
		queries:      queries,
		logger:       logger,
	}
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func correlationID(c *gin.Context) uuid.UUID {
	if existing, ok := c.Get(correlationContextKey); ok {
		if id, ok := existing.(uuid.UUID); ok {
			return id
		}
	}

	id := uuid.New()
	if raw := c.GetHeader(headerCorrelationID); raw != "" {
		if parsed, err := uuid.Parse(raw); err == nil {
			id = parsed
		}
	}

	c.Set(correlationContextKey, id)
	c.Writer.Header().Set(headerCorrelationID, id.String())
	return id
}

func (h *Handlers) OpenWallet(c *gin.Context) {
	identity, ok := identityFrom(c)
	if !ok {
		abortWith(c, http.StatusUnauthorized, CodeUnauthenticated, "missing or invalid credentials")
		return
	}
	if !identity.MayUseWalletOperations() {
		abortWith(c, http.StatusForbidden, CodeForbidden, "wallet operations are restricted to the internal service")
		return
	}

	var req openWalletRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		abortWith(c, http.StatusBadRequest, CodeInvalidRequest, "malformed request body")
		return
	}

	playerID, err := uuid.Parse(req.PlayerID)
	if err != nil {
		abortWith(c, http.StatusBadRequest, CodeInvalidRequest, "playerId must be a UUID")
		return
	}

	initial, err := req.InitialBalance.toMoney()
	if err != nil {
		respondError(c, err)
		return
	}

	result, err := h.openWallet.Execute(c.Request.Context(), app.OpenWalletCommand{
		PlayerID:       playerID,
		InitialBalance: initial,
		CorrelationID:  correlationID(c),
	})
	if err != nil {
		respondError(c, err)
		return
	}

	c.JSON(http.StatusCreated, walletOut(result.Wallet))
}

func (h *Handlers) GetWallet(c *gin.Context) {
	identity, ok := identityFrom(c)
	if !ok {
		abortWith(c, http.StatusUnauthorized, CodeUnauthenticated, "missing or invalid credentials")
		return
	}

	walletID, err := uuid.Parse(c.Param("walletId"))
	if err != nil {
		abortWith(c, http.StatusBadRequest, CodeInvalidRequest, "walletId must be a UUID")
		return
	}

	w, err := h.queries.GetWallet(c.Request.Context(), identity, walletID)
	if err != nil {
		respondError(c, err)
		return
	}

	c.JSON(http.StatusOK, walletOut(w))
}

func (h *Handlers) GetLedger(c *gin.Context) {
	identity, ok := identityFrom(c)
	if !ok {
		abortWith(c, http.StatusUnauthorized, CodeUnauthenticated, "missing or invalid credentials")
		return
	}

	walletID, err := uuid.Parse(c.Param("walletId"))
	if err != nil {
		abortWith(c, http.StatusBadRequest, CodeInvalidRequest, "walletId must be a UUID")
		return
	}

	limit := 50
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			abortWith(c, http.StatusBadRequest, CodeInvalidRequest, "limit must be a positive integer")
			return
		}
		limit = parsed
	}

	page, err := h.queries.GetLedger(c.Request.Context(), identity, walletID, c.Query("cursor"), limit)
	if err != nil {
		respondError(c, err)
		return
	}

	entries := make([]ledgerEntryResponse, 0, len(page.Entries))
	for _, e := range page.Entries {
		entries = append(entries, ledgerEntryResponse{
			ID:            e.ID().String(),
			WalletID:      e.WalletID().String(),
			TransactionID: e.TransactionID().String(),
			Direction:     string(e.Direction()),
			Money:         moneyOut(e.Amount()),
			BalanceBefore: moneyOut(e.BalanceBefore()),
			BalanceAfter:  moneyOut(e.BalanceAfter()),
			CreatedAt:     rfc3339(e.CreatedAt()),
		})
	}

	c.JSON(http.StatusOK, ledgerPageResponse{Entries: entries, NextCursor: page.NextCursor})
}

func (h *Handlers) Reconcile(c *gin.Context) {
	identity, ok := identityFrom(c)
	if !ok {
		abortWith(c, http.StatusUnauthorized, CodeUnauthenticated, "missing or invalid credentials")
		return
	}

	walletID, err := uuid.Parse(c.Param("walletId"))
	if err != nil {
		abortWith(c, http.StatusBadRequest, CodeInvalidRequest, "walletId must be a UUID")
		return
	}

	result, err := h.queries.Reconcile(c.Request.Context(), identity, walletID)
	if err != nil {
		respondError(c, err)
		return
	}

	if !result.Consistent {
		h.logger.Error("wallet reconciliation divergence",
			zap.String("walletId", result.WalletID.String()),
			zap.String("storedBalance", result.StoredBalance.DecimalString()),
			zap.String("calculatedBalance", result.CalculatedBalance.DecimalString()),
			zap.String("difference", result.Difference.DecimalString()),
			zap.Int("checkedEntries", result.CheckedEntries),
		)
		observeReconciliationDivergence()
	}

	c.JSON(http.StatusOK, reconciliationResponse{
		WalletID:          result.WalletID.String(),
		StoredBalance:     moneyOut(result.StoredBalance),
		CalculatedBalance: moneyOut(result.CalculatedBalance),
		Difference:        moneyOut(result.Difference),
		Consistent:        result.Consistent,
		CheckedEntries:    result.CheckedEntries,
	})
}

func (h *Handlers) SubmitTransaction(c *gin.Context) {
	identity, ok := identityFrom(c)
	if !ok {
		abortWith(c, http.StatusUnauthorized, CodeUnauthenticated, "missing or invalid credentials")
		return
	}

	idempotencyKey := c.GetHeader(headerIdempotencyKey)
	if idempotencyKey == "" {
		abortWith(c, http.StatusBadRequest, CodeInvalidRequest, "the Idempotency-Key header is required")
		return
	}

	var req submitTransactionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		abortWith(c, http.StatusBadRequest, CodeInvalidRequest, "malformed request body")
		return
	}

	if !identity.MayActAsProvider(req.ProviderID) {
		abortWith(c, http.StatusForbidden, CodeForbidden, "not authorized to act as this provider")
		return
	}

	cmd, err := buildProcessCommand(req, idempotencyKey, correlationID(c))
	if err != nil {
		respondError(c, err)
		return
	}

	started := time.Now()
	result, err := h.processWager.Execute(c.Request.Context(), cmd)
	observeSubmission(started, result, err)
	h.logOutcome(cmd, result, err, "http")
	if err != nil {
		respondError(c, err)
		return
	}

	respondSubmitResult(c, result)
}

func (h *Handlers) logOutcome(cmd app.ProcessWagerCommand, result app.ProcessWagerResult, err error, source string) {
	fields := []zap.Field{
		zap.String("source", source),
		zap.String("correlationId", cmd.CorrelationID.String()),
		zap.String("providerId", cmd.ProviderID),
		zap.String("externalTransactionId", cmd.ExternalTransactionID),
		zap.String("walletId", cmd.WalletID.String()),
		zap.String("playerId", cmd.PlayerID.String()),
		zap.String("kind", string(cmd.Kind)),
	}
	if cmd.MessageID != "" {
		fields = append(fields, zap.String("messageId", cmd.MessageID))
	}

	if err != nil {
		h.logger.Warn("wager operation not applied", append(fields, zap.Error(err))...)
		return
	}

	fields = append(fields,
		zap.String("transactionId", result.Transaction.ID().String()),
		zap.String("status", string(result.Transaction.Status())),
		zap.Bool("idempotentReplay", result.IdempotentReplay),
	)
	if code := result.Transaction.FailureCode(); code != "" {
		fields = append(fields, zap.String("failureCode", string(code)))
	}

	h.logger.Info("wager operation settled", fields...)
}

func buildProcessCommand(req submitTransactionRequest, idempotencyKey string, correlation uuid.UUID) (app.ProcessWagerCommand, error) {
	playerID, err := uuid.Parse(req.PlayerID)
	if err != nil {
		return app.ProcessWagerCommand{}, errInvalidField("playerId must be a UUID")
	}
	walletID, err := uuid.Parse(req.WalletID)
	if err != nil {
		return app.ProcessWagerCommand{}, errInvalidField("walletId must be a UUID")
	}

	kind, err := parseKind(req.Kind)
	if err != nil {
		return app.ProcessWagerCommand{}, err
	}

	amount, err := req.Money.toMoney()
	if err != nil {
		return app.ProcessWagerCommand{}, err
	}

	hash, err := app.PayloadHash(app.PayloadFields{
		ProviderID:                     req.ProviderID,
		ExternalTransactionID:          req.ExternalTransactionID,
		PlayerID:                       req.PlayerID,
		WalletID:                       req.WalletID,
		RoundID:                        req.RoundID,
		GameID:                         req.GameID,
		Kind:                           string(kind),
		MoneyAmount:                    amount.DecimalString(),
		MoneyCurrency:                  string(amount.Currency()),
		ReferenceExternalTransactionID: req.ReferenceExternalTransactionID,
	})
	if err != nil {
		return app.ProcessWagerCommand{}, err
	}

	return app.ProcessWagerCommand{
		ProviderID:                     req.ProviderID,
		ExternalTransactionID:          req.ExternalTransactionID,
		IdempotencyKey:                 idempotencyKey,
		PayloadHash:                    hash,
		PlayerID:                       playerID,
		WalletID:                       walletID,
		RoundID:                        req.RoundID,
		GameID:                         req.GameID,
		Kind:                           kind,
		Money:                          amount,
		ReferenceExternalTransactionID: req.ReferenceExternalTransactionID,
		CorrelationID:                  correlation,
	}, nil
}

func respondSubmitResult(c *gin.Context, result app.ProcessWagerResult) {
	body := submitOut(result.Transaction, result.Balance, result.IdempotentReplay)

	switch result.Transaction.Status() {
	case wagertx.Processed:
		c.JSON(http.StatusOK, body)
	case wagertx.Rejected:
		c.JSON(http.StatusUnprocessableEntity, body)
	case wagertx.PendingReference, wagertx.Pending:
		c.JSON(http.StatusAccepted, body)
	default:
		c.JSON(http.StatusOK, body)
	}
}

func (h *Handlers) GetTransaction(c *gin.Context) {
	identity, ok := identityFrom(c)
	if !ok {
		abortWith(c, http.StatusUnauthorized, CodeUnauthenticated, "missing or invalid credentials")
		return
	}

	transactionID, err := uuid.Parse(c.Param("transactionId"))
	if err != nil {
		abortWith(c, http.StatusBadRequest, CodeInvalidRequest, "transactionId must be a UUID")
		return
	}

	tx, err := h.queries.GetTransaction(c.Request.Context(), identity, transactionID)
	if err != nil {
		respondError(c, err)
		return
	}

	c.JSON(http.StatusOK, transactionOut(tx))
}

func (h *Handlers) GetProviderTransaction(c *gin.Context) {
	identity, ok := identityFrom(c)
	if !ok {
		abortWith(c, http.StatusUnauthorized, CodeUnauthenticated, "missing or invalid credentials")
		return
	}

	tx, err := h.queries.GetProviderTransaction(
		c.Request.Context(), identity,
		c.Param("providerId"), c.Param("externalTransactionId"),
	)
	if err != nil {
		respondError(c, err)
		return
	}

	c.JSON(http.StatusOK, transactionOut(tx))
}

type invalidFieldError struct{ msg string }

func (e invalidFieldError) Error() string { return e.msg }

func errInvalidField(msg string) error { return invalidFieldError{msg: msg} }
