package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/fx"
	"go.uber.org/zap"

	"jungle/internal/app"
	"jungle/internal/config"
	"jungle/internal/router"
)

func NewRouteSet(h *Handlers, verifier TokenVerifier, logger *zap.Logger) router.Set {
	return router.Set{
		Middlewares: []gin.HandlerFunc{Authenticate(verifier, logger)},
		Routes: []router.Route{
			router.Simple(http.MethodPost, "/wallets", h.OpenWallet),
			router.Simple(http.MethodGet, "/wallets/:walletId", h.GetWallet),
			router.Simple(http.MethodGet, "/wallets/:walletId/ledger", h.GetLedger),
			router.Simple(http.MethodPost, "/wallets/:walletId/reconciliation", h.Reconcile),

			router.Simple(http.MethodPost, "/wagering/transactions", h.SubmitTransaction),
			router.Simple(http.MethodGet, "/wagering/transactions/:transactionId", h.GetTransaction),
			router.Simple(http.MethodGet, "/providers/:providerId/wagering/transactions/:externalTransactionId", h.GetProviderTransaction),
		},
	}
}

func newVerifier(lc fx.Lifecycle, cfg *config.Config, logger *zap.Logger) (TokenVerifier, error) {
	var verifier TokenVerifier

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			v, err := NewOIDCVerifier(ctx, cfg, logger)
			if err != nil {
				return err
			}
			verifier = v
			logger.Info("OIDC issuer reachable",
				zap.String("issuer", cfg.OIDC.IssuerURL),
				zap.String("audience", cfg.OIDC.Audience),
			)
			return nil
		},
	})

	return &lazyVerifier{get: func() TokenVerifier { return verifier }}, nil
}

type lazyVerifier struct {
	get func() TokenVerifier
}

func (l *lazyVerifier) Verify(ctx context.Context, rawToken string) (app.Identity, error) {
	v := l.get()
	if v == nil {
		return app.Identity{}, errVerifierNotReady
	}
	return v.Verify(ctx, rawToken)
}

var errVerifierNotReady = errors.New("httpapi: token verifier not initialized yet")

var Module = fx.Module("httpapi",
	fx.Provide(
		NewHandlers,
		newVerifier,
		router.AsRouteSet(NewRouteSet),
	),
)
