package app

import (
	"context"
	"time"

	"github.com/google/uuid"

	"jungle/internal/domain/event"
	"jungle/internal/domain/journal"
	"jungle/internal/domain/money"
	"jungle/internal/domain/wagertx"
	"jungle/internal/domain/wallet"
)

type WalletRepository interface {
	Insert(ctx context.Context, w wallet.Wallet) error

	Get(ctx context.Context, id uuid.UUID) (wallet.Wallet, error)

	GetForUpdate(ctx context.Context, id uuid.UUID) (wallet.Wallet, error)

	UpdateBalance(ctx context.Context, w wallet.Wallet, expectedVersion int64) error

	ExistsForPlayerCurrency(ctx context.Context, playerID uuid.UUID, currency money.Currency) (bool, error)
}

type TransactionRepository interface {
	Insert(ctx context.Context, tx wagertx.WagerTransaction) error

	Update(ctx context.Context, tx wagertx.WagerTransaction) error

	GetByID(ctx context.Context, id uuid.UUID) (wagertx.WagerTransaction, error)
	GetByIdempotencyKey(ctx context.Context, key string) (wagertx.WagerTransaction, error)
	GetByProviderExternalID(ctx context.Context, providerID, externalID string) (wagertx.WagerTransaction, error)

	HasSuccessfulReversal(ctx context.Context, referenceID uuid.UUID) (bool, error)

	ClaimPendingReference(ctx context.Context, limit int, now time.Time) ([]PendingReferenceRecord, error)

	RecordReferenceAttempt(ctx context.Context, id uuid.UUID, attempts int, nextAttemptAt time.Time) error

	CountPendingReference(ctx context.Context) (int, error)
}

type PendingReferenceRecord struct {
	Transaction wagertx.WagerTransaction
	Attempts    int
	FirstSeenAt time.Time
}

type LedgerRepository interface {
	Insert(ctx context.Context, entry wallet.WalletLedgerEntry) error

	Page(ctx context.Context, walletID uuid.UUID, cursor string, limit int) ([]wallet.WalletLedgerEntry, string, error)

	Totals(ctx context.Context, walletID uuid.UUID) (credits, debits int64, entries int, err error)
}

type InboxRecord struct {
	ConsumerName       string
	MessageID          string
	PayloadHash        string
	WagerTransactionID uuid.UUID
	ReceivedAt         time.Time
	CompletedAt        time.Time
}

type InboxRepository interface {
	Get(ctx context.Context, consumerName, messageID string) (InboxRecord, bool, error)
	Insert(ctx context.Context, rec InboxRecord) error
}

type OutboxRecord struct {
	EventID uuid.UUID

	TraceParent string

	AggregateID string

	Type     string
	Payload  []byte
	Attempts int
}

type OutboxRepository interface {
	Insert(ctx context.Context, events ...event.Envelope) error

	Claim(ctx context.Context, owner string, limit int, lockFor time.Duration, now time.Time) ([]OutboxRecord, error)

	MarkPublished(ctx context.Context, eventID uuid.UUID, now time.Time) error

	Reschedule(ctx context.Context, eventID uuid.UUID, attempts int, nextAttemptAt time.Time) error

	OldestPending(ctx context.Context, now time.Time) (time.Duration, bool, error)
}

type JournalRepository interface {
	Insert(ctx context.Context, entries ...journal.Entry) error
	ByTransaction(ctx context.Context, transactionID uuid.UUID) ([]journal.Entry, error)
	GlobalImbalance(ctx context.Context) (int64, error)
	AccountBalance(ctx context.Context, account journal.Account) (int64, error)
}

type Repositories interface {
	Wallets() WalletRepository
	Transactions() TransactionRepository
	Ledger() LedgerRepository
	Inbox() InboxRepository
	Outbox() OutboxRepository
	Journal() JournalRepository
}

type UnitOfWork interface {
	Within(ctx context.Context, fn func(ctx context.Context, repos Repositories) error) error
}

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }
