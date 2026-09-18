package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"jungle/internal/app"
	"jungle/internal/config"
)

const identityContextKey = "jungle.identity"

const (
	roleInternalService = "internal-service"
	roleProvider        = "provider"
)

func identityFrom(c *gin.Context) (app.Identity, bool) {
	raw, exists := c.Get(identityContextKey)
	if !exists {
		return app.Identity{}, false
	}
	identity, ok := raw.(app.Identity)
	return identity, ok
}

type TokenVerifier interface {
	Verify(ctx context.Context, rawToken string) (app.Identity, error)
}

type OIDCVerifier struct {
	verifier *oidc.IDTokenVerifier

	acceptedIssuers map[string]bool
}

type tokenClaims struct {
	RealmAccess struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
	ProviderID       string `json:"provider_id"`
	AuthorizedParty  string `json:"azp"`
	PreferredUsrname string `json:"preferred_username"`
}

func NewOIDCVerifier(ctx context.Context, cfg *config.Config, logger *zap.Logger) (*OIDCVerifier, error) {
	var provider *oidc.Provider
	var err error

	deadline := time.Now().Add(cfg.OIDC.DiscoveryTimeout)
	for attempt := 1; ; attempt++ {
		provider, err = oidc.NewProvider(ctx, cfg.OIDC.IssuerURL)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("discovering OIDC issuer %s: %w", cfg.OIDC.IssuerURL, err)
		}
		logger.Warn("OIDC issuer not ready, retrying",
			zap.String("issuer", cfg.OIDC.IssuerURL),
			zap.Int("attempt", attempt),
			zap.Error(err),
		)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}

	accepted := map[string]bool{cfg.OIDC.IssuerURL: true}
	for _, issuer := range cfg.OIDC.AdditionalIssuers {
		accepted[issuer] = true
	}

	return &OIDCVerifier{
		verifier: provider.Verifier(&oidc.Config{
			ClientID: cfg.OIDC.Audience,

			SkipIssuerCheck: true,
		}),
		acceptedIssuers: accepted,
	}, nil
}

func (v *OIDCVerifier) Verify(ctx context.Context, rawToken string) (app.Identity, error) {
	token, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return app.Identity{}, fmt.Errorf("verifying token: %w", err)
	}

	if !v.acceptedIssuers[token.Issuer] {
		return app.Identity{}, fmt.Errorf("token issuer %q is not trusted", token.Issuer)
	}

	var claims tokenClaims
	if err := token.Claims(&claims); err != nil {
		return app.Identity{}, fmt.Errorf("reading token claims: %w", err)
	}

	for _, role := range claims.RealmAccess.Roles {
		switch role {
		case roleInternalService:
			return app.Identity{Internal: true}, nil
		case roleProvider:
			if claims.ProviderID == "" {
				return app.Identity{}, fmt.Errorf("token has the %s role but no provider_id claim", roleProvider)
			}
			return app.Identity{ProviderID: claims.ProviderID}, nil
		}
	}

	return app.Identity{}, fmt.Errorf("token carries no role this service recognizes")
}

func Authenticate(verifier TokenVerifier, logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok {
			observeAuthFailure("missing")
			abortWith(c, http.StatusUnauthorized, CodeUnauthenticated, "a bearer token is required")
			return
		}

		identity, err := verifier.Verify(c.Request.Context(), raw)
		if err != nil {
			logger.Warn("rejected token", zap.Error(err))
			observeAuthFailure("invalid")
			abortWith(c, http.StatusUnauthorized, CodeUnauthenticated, "invalid or expired credentials")
			return
		}

		c.Set(identityContextKey, identity)
		c.Next()
	}
}

func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}
