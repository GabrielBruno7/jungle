//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"jungle/internal/app"
	"jungle/internal/domain/money"
	"jungle/internal/domain/wagertx"
	"jungle/internal/postgres"
)

const (
	defaultDSN         = "postgres://jungle:jungle@localhost:5432/jungle?sslmode=disable"
	defaultKeycloakURL = "http://localhost:8081"
	testRealm          = "jungle"
)

func dsn() string {
	if v := os.Getenv("TEST_POSTGRES_DSN"); v != "" {
		return v
	}
	return defaultDSN
}

func keycloakURL() string {
	if v := os.Getenv("TEST_KEYCLOAK_URL"); v != "" {
		return v
	}
	return defaultKeycloakURL
}

func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool, err := pgxpool.New(context.Background(), dsn())
	if err != nil {
		t.Fatalf("connecting to postgres at %s: %v (is `docker compose up -d postgres` running?)", dsn(), err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("pinging postgres: %v (is `docker compose up -d postgres` running, and have migrations been applied?)", err)
	}

	t.Cleanup(pool.Close)
	return pool
}

type instance struct {
	pool      *pgxpool.Pool
	repos     *postgres.Repos
	uow       *postgres.UnitOfWork
	open      *app.OpenWallet
	process   *app.ProcessWager
	queries   *app.Queries
	resolve   *app.ResolveReferences
	publisher *recordingPublisher
	outbox    *app.PublishOutbox
}

func newInstance(t *testing.T, name string) *instance {
	t.Helper()

	pool := newPool(t)
	repos := postgres.NewRepos(pool)
	uow := postgres.NewUnitOfWork(pool)
	clock := app.SystemClock{}

	process := app.NewProcessWager(uow, repos, clock)
	publisher := &recordingPublisher{}

	return &instance{
		pool:      pool,
		repos:     repos,
		uow:       uow,
		open:      app.NewOpenWallet(uow, clock),
		process:   process,
		queries:   app.NewQueries(repos, uow),
		resolve:   app.NewResolveReferences(uow, process, clock, app.DefaultReferencePolicy(), zap.NewNop()),
		publisher: publisher,
		outbox:    app.NewPublishOutbox(uow, publisher, clock, app.InstanceID(name), app.DefaultPublishPolicy()),
	}
}

var internalIdentity = app.Identity{Internal: true}

func providerIdentity(id string) app.Identity { return app.Identity{ProviderID: id} }

func brl(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, money.BRL)
	if err != nil {
		t.Fatalf("parsing %q: %v", amount, err)
	}
	return m
}

func (i *instance) openWallet(t *testing.T, amount string) (uuid.UUID, uuid.UUID) {
	t.Helper()

	playerID := uuid.New()
	result, err := i.open.Execute(context.Background(), app.OpenWalletCommand{
		PlayerID:       playerID,
		InitialBalance: brl(t, amount),
		CorrelationID:  uuid.New(),
	})
	if err != nil {
		t.Fatalf("opening wallet: %v", err)
	}
	return result.Wallet.ID(), playerID
}

type operation struct {
	providerID string
	externalID string
	key        string
	playerID   uuid.UUID
	walletID   uuid.UUID
	roundID    string
	gameID     string
	kind       wagertx.Kind
	amount     string
	reference  string
	messageID  string
}

func (o operation) command(t *testing.T) app.ProcessWagerCommand {
	t.Helper()

	provider := o.providerID
	if provider == "" {
		provider = "provider-a"
	}
	round := o.roundID
	if round == "" {
		round = "round-1"
	}
	game := o.gameID
	if game == "" {
		game = "fortune-chimp"
	}
	key := o.key
	if key == "" {
		key = provider + ":" + o.externalID
	}

	amount := brl(t, o.amount)
	hash, err := app.PayloadHash(app.PayloadFields{
		ProviderID:                     provider,
		ExternalTransactionID:          o.externalID,
		PlayerID:                       o.playerID.String(),
		WalletID:                       o.walletID.String(),
		RoundID:                        round,
		GameID:                         game,
		Kind:                           string(o.kind),
		MoneyAmount:                    amount.DecimalString(),
		MoneyCurrency:                  string(amount.Currency()),
		ReferenceExternalTransactionID: o.reference,
	})
	if err != nil {
		t.Fatalf("hashing payload: %v", err)
	}

	cmd := app.ProcessWagerCommand{
		ProviderID:                     provider,
		ExternalTransactionID:          o.externalID,
		IdempotencyKey:                 key,
		PayloadHash:                    hash,
		PlayerID:                       o.playerID,
		WalletID:                       o.walletID,
		RoundID:                        round,
		GameID:                         game,
		Kind:                           o.kind,
		Money:                          amount,
		ReferenceExternalTransactionID: o.reference,
		CorrelationID:                  uuid.New(),
	}
	if o.messageID != "" {
		cmd.ConsumerName = "integration-test-consumer"
		cmd.MessageID = o.messageID
	}
	return cmd
}

func (i *instance) submit(t *testing.T, op operation) (app.ProcessWagerResult, error) {
	t.Helper()
	return i.process.Execute(context.Background(), op.command(t))
}

func (i *instance) mustSubmit(t *testing.T, op operation) app.ProcessWagerResult {
	t.Helper()
	result, err := i.submit(t, op)
	if err != nil {
		t.Fatalf("submitting %s %s: %v", op.kind, op.externalID, err)
	}
	return result
}

func (i *instance) balance(t *testing.T, walletID uuid.UUID) string {
	t.Helper()
	w, err := i.queries.GetWallet(context.Background(), internalIdentity, walletID)
	if err != nil {
		t.Fatalf("reading wallet: %v", err)
	}
	return w.Balance().DecimalString()
}

func (i *instance) ledgerCount(t *testing.T, walletID uuid.UUID) int {
	t.Helper()

	var count int
	err := i.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM wallet_ledger_entry WHERE wallet_id = $1`, walletID).Scan(&count)
	if err != nil {
		t.Fatalf("counting ledger entries: %v", err)
	}
	return count
}

type recordingPublisher struct {
	mu        chan struct{}
	published []app.OutboxRecord
	failNext  bool
}

func (p *recordingPublisher) lock() {
	if p.mu == nil {
		p.mu = make(chan struct{}, 1)
	}
	p.mu <- struct{}{}
}

func (p *recordingPublisher) unlock() { <-p.mu }

func (p *recordingPublisher) Publish(_ context.Context, rec app.OutboxRecord) error {
	p.lock()
	defer p.unlock()

	if p.failNext {
		return fmt.Errorf("simulated publish failure")
	}
	p.published = append(p.published, rec)
	return nil
}

func (p *recordingPublisher) eventIDs() []string {
	p.lock()
	defer p.unlock()

	ids := make([]string, 0, len(p.published))
	for _, rec := range p.published {
		ids = append(ids, rec.EventID.String())
	}
	return ids
}

func (i *instance) resolverWith(policy app.ReferencePolicy) *app.ResolveReferences {
	return app.NewResolveReferences(i.uow, i.process, app.SystemClock{}, policy, zap.NewNop())
}

func (i *instance) shutdown() { i.pool.Close() }

func tryFetchToken(t *testing.T, clientID, clientSecret string) (string, bool) {
	t.Helper()

	endpoint := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", keycloakURL(), testRealm)
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}

	resp, err := http.Post(endpoint, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("requesting token from %s: %v (is `docker compose up -d keycloak` running?)", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return "", false
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token endpoint returned %d for client %s", resp.StatusCode, clientID)
	}

	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding token response: %v", err)
	}
	return body.AccessToken, body.AccessToken != ""
}

func fetchToken(t *testing.T, clientID, clientSecret string) string {
	t.Helper()

	endpoint := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", keycloakURL(), testRealm)
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}

	resp, err := http.Post(endpoint, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("requesting token from %s: %v (is `docker compose up -d keycloak` running?)", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token endpoint returned %d for client %s", resp.StatusCode, clientID)
	}

	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding token response: %v", err)
	}
	if body.AccessToken == "" {
		t.Fatalf("token endpoint returned an empty access_token for client %s", clientID)
	}
	return body.AccessToken
}
