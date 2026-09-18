//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"jungle/internal/app"
	"jungle/internal/config"
	"jungle/internal/httpapi"
	"jungle/internal/router"
)

type testServer struct {
	engine   *gin.Engine
	instance *instance
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()

	inst := newInstance(t, "http-test")
	logger := zap.NewNop()

	cfg := config.New()
	cfg.OIDC.IssuerURL = keycloakURL() + "/realms/" + testRealm
	cfg.OIDC.Audience = "jungle-api"
	cfg.OIDC.DiscoveryTimeout = 30 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	verifier, err := httpapi.NewOIDCVerifier(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("discovering the IdP at %s: %v (is `docker compose up -d keycloak` running?)", cfg.OIDC.IssuerURL, err)
	}

	handlers := httpapi.NewHandlers(inst.open, inst.process, inst.queries, logger)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	router.Register(router.Params{
		Engine:    engine,
		RouteSets: []router.Set{httpapi.NewRouteSet(handlers, verifier, logger)},
	})

	return &testServer{engine: engine, instance: inst}
}

func (s *testServer) do(t *testing.T, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var payload *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encoding request body: %v", err)
		}
		payload = bytes.NewReader(encoded)
	} else {
		payload = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, path, payload)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	s.engine.ServeHTTP(rec, req)
	return rec
}

func TestAuth_RejectsMissingAndInvalidCredentials(t *testing.T) {
	s := newTestServer(t)

	cases := []struct {
		name  string
		token string
	}{
		{"no token at all", ""},
		{"a token that is not a JWT", "not-a-token"},
		{"a syntactically valid but unsigned JWT", "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJhdHRhY2tlciJ9.c2lnbmF0dXJl"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := s.do(t, http.MethodGet, "/wallets/"+uuid.NewString(), tc.token, nil)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401 (body: %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAuth_ForeignTokenIsRejected(t *testing.T) {
	s := newTestServer(t)

	foreign := masterRealmToken(t)

	rec := s.do(t, http.MethodGet, "/wallets/"+uuid.NewString(), foreign, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a token from another realm (body: %s)", rec.Code, rec.Body.String())
	}
}

func masterRealmToken(t *testing.T) string {
	t.Helper()

	endpoint := fmt.Sprintf("%s/realms/master/protocol/openid-connect/token", keycloakURL())
	resp, err := http.PostForm(endpoint, map[string][]string{
		"grant_type": {"password"},
		"client_id":  {"admin-cli"},
		"username":   {"admin"},
		"password":   {"admin"},
	})
	if err != nil {
		t.Fatalf("requesting a master-realm token: %v", err)
	}
	defer resp.Body.Close()

	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding master-realm token: %v", err)
	}
	return body.AccessToken
}

func TestAuth_WalletOperationsAreInternalOnly(t *testing.T) {
	s := newTestServer(t)
	internal := fetchToken(t, "jungle-internal", "internal-secret")
	provider := fetchToken(t, "provider-a", "provider-a-secret")

	openBody := map[string]any{
		"playerId":       uuid.NewString(),
		"initialBalance": map[string]string{"amount": "100.00", "currency": "BRL"},
	}

	rec := s.do(t, http.MethodPost, "/wallets", provider, openBody)
	if rec.Code != http.StatusForbidden {
		t.Errorf("provider opening a wallet: status = %d, want 403 (body: %s)", rec.Code, rec.Body.String())
	}

	rec = s.do(t, http.MethodPost, "/wallets", internal, openBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("internal opening a wallet: status = %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestAuth_ProviderCannotActAsAnother(t *testing.T) {
	s := newTestServer(t)
	internal := fetchToken(t, "jungle-internal", "internal-secret")
	providerA := fetchToken(t, "provider-a", "provider-a-secret")

	playerID := uuid.NewString()
	rec := s.do(t, http.MethodPost, "/wallets", internal, map[string]any{
		"playerId":       playerID,
		"initialBalance": map[string]string{"amount": "100.00", "currency": "BRL"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("opening wallet: status = %d (body: %s)", rec.Code, rec.Body.String())
	}
	var wallet struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &wallet); err != nil {
		t.Fatalf("decoding wallet: %v", err)
	}

	suffix := uuid.NewString()[:8]
	impersonation := map[string]any{
		"providerId":            "provider-b",
		"externalTransactionId": "spoof-" + suffix,
		"playerId":              playerID,
		"walletId":              wallet.ID,
		"roundId":               "r1",
		"gameId":                "g1",
		"kind":                  "BET",
		"money":                 map[string]string{"amount": "10.00", "currency": "BRL"},
	}

	req := httptest.NewRequest(http.MethodPost, "/wagering/transactions", bytes.NewReader(mustJSON(t, impersonation)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+providerA)
	req.Header.Set("Idempotency-Key", "provider-b:spoof-"+suffix)

	rec = httptest.NewRecorder()
	s.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 when acting as another provider (body: %s)", rec.Code, rec.Body.String())
	}

	walletID := uuid.MustParse(wallet.ID)
	if got := s.instance.balance(t, walletID); got != "100.00" {
		t.Errorf("balance = %s, want 100.00 — an unauthorized call must have no financial effect", got)
	}
}

func TestHTTP_IdempotencyKeyIsRequired(t *testing.T) {
	s := newTestServer(t)
	provider := fetchToken(t, "provider-a", "provider-a-secret")

	body := map[string]any{
		"providerId":            "provider-a",
		"externalTransactionId": "no-key-" + uuid.NewString()[:8],
		"playerId":              uuid.NewString(),
		"walletId":              uuid.NewString(),
		"roundId":               "r1",
		"gameId":                "g1",
		"kind":                  "BET",
		"money":                 map[string]string{"amount": "10.00", "currency": "BRL"},
	}

	rec := s.do(t, http.MethodPost, "/wagering/transactions", provider, body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 without an Idempotency-Key (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestHTTP_OpeningKindIsRefusedFromProviders(t *testing.T) {
	s := newTestServer(t)
	internal := fetchToken(t, "jungle-internal", "internal-secret")
	provider := fetchToken(t, "provider-a", "provider-a-secret")

	playerID := uuid.NewString()
	rec := s.do(t, http.MethodPost, "/wallets", internal, map[string]any{
		"playerId":       playerID,
		"initialBalance": map[string]string{"amount": "100.00", "currency": "BRL"},
	})
	var wallet struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &wallet); err != nil {
		t.Fatalf("decoding wallet: %v", err)
	}

	suffix := uuid.NewString()[:8]
	req := httptest.NewRequest(http.MethodPost, "/wagering/transactions", bytes.NewReader(mustJSON(t, map[string]any{
		"providerId":            "provider-a",
		"externalTransactionId": "opening-" + suffix,
		"playerId":              playerID,
		"walletId":              wallet.ID,
		"roundId":               "r1",
		"gameId":                "g1",
		"kind":                  "OPENING",
		"money":                 map[string]string{"amount": "1000000.00", "currency": "BRL"},
	})))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider)
	req.Header.Set("Idempotency-Key", "provider-a:opening-"+suffix)

	rec = httptest.NewRecorder()
	s.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an OPENING submitted by a provider (body: %s)", rec.Code, rec.Body.String())
	}
	if got := s.instance.balance(t, uuid.MustParse(wallet.ID)); got != "100.00" {
		t.Errorf("balance = %s, want 100.00 — the refused OPENING must not have credited anything", got)
	}
}

func TestHTTP_StatusCodesPerOutcome(t *testing.T) {
	s := newTestServer(t)
	internal := fetchToken(t, "jungle-internal", "internal-secret")
	provider := fetchToken(t, "provider-a", "provider-a-secret")

	playerID := uuid.NewString()
	rec := s.do(t, http.MethodPost, "/wallets", internal, map[string]any{
		"playerId":       playerID,
		"initialBalance": map[string]string{"amount": "50.00", "currency": "BRL"},
	})
	var wallet struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &wallet); err != nil {
		t.Fatalf("decoding wallet: %v", err)
	}

	suffix := uuid.NewString()[:8]

	submit := func(externalID, kind, amount, reference string) *httptest.ResponseRecorder {
		body := map[string]any{
			"providerId":            "provider-a",
			"externalTransactionId": externalID,
			"playerId":              playerID,
			"walletId":              wallet.ID,
			"roundId":               "r1",
			"gameId":                "g1",
			"kind":                  kind,
			"money":                 map[string]string{"amount": amount, "currency": "BRL"},
		}
		if reference != "" {
			body["referenceExternalTransactionId"] = reference
		}
		req := httptest.NewRequest(http.MethodPost, "/wagering/transactions", bytes.NewReader(mustJSON(t, body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+provider)
		req.Header.Set("Idempotency-Key", "provider-a:"+externalID)

		out := httptest.NewRecorder()
		s.engine.ServeHTTP(out, req)
		return out
	}

	if got := submit("ok-"+suffix, "BET", "10.00", "").Code; got != http.StatusOK {
		t.Errorf("processed bet: status = %d, want 200", got)
	}
	if got := submit("broke-"+suffix, "BET", "9999.00", "").Code; got != http.StatusUnprocessableEntity {
		t.Errorf("rejected bet: status = %d, want 422", got)
	}
	if got := submit("pending-"+suffix, "REFUND", "5.00", "does-not-exist-"+suffix).Code; got != http.StatusAccepted {
		t.Errorf("pending reference: status = %d, want 202", got)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encoding json: %v", err)
	}
	return encoded
}

var _ = app.Identity{}
