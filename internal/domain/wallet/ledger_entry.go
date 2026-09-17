package wallet

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"jungle/internal/domain/money"
)

// LedgerDirection is the direction of a WalletLedgerEntry: money leaving
// (Debit) or entering (Credit) the wallet.
type LedgerDirection string

const (
	Debit  LedgerDirection = "DEBIT"
	Credit LedgerDirection = "CREDIT"
)

var (
	ErrInvalidDirection   = errors.New("wallet: ledger direction must be DEBIT or CREDIT")
	ErrLedgerInvariant    = errors.New("wallet: balanceAfter does not match balanceBefore and direction")
	ErrInvalidLedgerEntry = errors.New("wallet: invalid ledger entry")
)

// WalletLedgerEntry is a single, immutable line of the append-only ledger:
// one financial movement, tied to the WagerTransaction that caused it,
// carrying the exact balance snapshot before and after. It is never
// edited or deleted once created — a correction is always a new entry,
// never a change to an existing one.
type WalletLedgerEntry struct {
	id            uuid.UUID
	walletID      uuid.UUID
	transactionID uuid.UUID
	direction     LedgerDirection
	amount        money.Money
	balanceBefore money.Money
	balanceAfter  money.Money
	createdAt     time.Time
}

// NewWalletLedgerEntryParams carries the arguments to record a brand-new
// ledger entry for a movement that just happened.
type NewWalletLedgerEntryParams struct {
	ID            uuid.UUID
	WalletID      uuid.UUID
	TransactionID uuid.UUID
	Direction     LedgerDirection
	Amount        money.Money
	BalanceBefore money.Money
	Now           time.Time
}

// NewWalletLedgerEntry derives balanceAfter from BalanceBefore, Direction
// and Amount itself — the caller cannot pass in a wrong balanceAfter by
// mistake — and returns an error rather than ever producing an
// inconsistent snapshot.
func NewWalletLedgerEntry(p NewWalletLedgerEntryParams) (WalletLedgerEntry, error) {
	if p.ID == uuid.Nil || p.WalletID == uuid.Nil || p.TransactionID == uuid.Nil {
		return WalletLedgerEntry{}, fmt.Errorf("%w: missing identity fields", ErrInvalidLedgerEntry)
	}
	if !p.Amount.IsPositive() {
		return WalletLedgerEntry{}, fmt.Errorf("%w: amount must be positive", ErrInvalidLedgerEntry)
	}
	if p.Now.IsZero() {
		return WalletLedgerEntry{}, fmt.Errorf("%w: now is required", ErrInvalidLedgerEntry)
	}

	balanceAfter, err := applyDirection(p.BalanceBefore, p.Amount, p.Direction)
	if err != nil {
		return WalletLedgerEntry{}, err
	}

	return WalletLedgerEntry{
		id:            p.ID,
		walletID:      p.WalletID,
		transactionID: p.TransactionID,
		direction:     p.Direction,
		amount:        p.Amount,
		balanceBefore: p.BalanceBefore,
		balanceAfter:  balanceAfter,
		createdAt:     p.Now,
	}, nil
}

// RehydrateWalletLedgerEntryParams carries the exact persisted state of a
// ledger entry, including its already-computed balanceAfter.
type RehydrateWalletLedgerEntryParams struct {
	ID            uuid.UUID
	WalletID      uuid.UUID
	TransactionID uuid.UUID
	Direction     LedgerDirection
	Amount        money.Money
	BalanceBefore money.Money
	BalanceAfter  money.Money
	CreatedAt     time.Time
}

// RehydrateWalletLedgerEntry reconstructs a persisted ledger entry. It
// re-derives balanceAfter from BalanceBefore/Amount/Direction and compares
// it against the persisted BalanceAfter — a cheap, pure re-verification
// that catches storage-level corruption, not a replay of any side effect.
func RehydrateWalletLedgerEntry(p RehydrateWalletLedgerEntryParams) (WalletLedgerEntry, error) {
	expected, err := applyDirection(p.BalanceBefore, p.Amount, p.Direction)
	if err != nil {
		return WalletLedgerEntry{}, err
	}
	if !expected.Equal(p.BalanceAfter) {
		return WalletLedgerEntry{}, ErrLedgerInvariant
	}

	return WalletLedgerEntry{
		id:            p.ID,
		walletID:      p.WalletID,
		transactionID: p.TransactionID,
		direction:     p.Direction,
		amount:        p.Amount,
		balanceBefore: p.BalanceBefore,
		balanceAfter:  p.BalanceAfter,
		createdAt:     p.CreatedAt,
	}, nil
}

func applyDirection(before, amount money.Money, direction LedgerDirection) (money.Money, error) {
	switch direction {
	case Debit:
		return before.Sub(amount)
	case Credit:
		return before.Add(amount)
	default:
		return money.Money{}, fmt.Errorf("%w: %q", ErrInvalidDirection, direction)
	}
}

func (e WalletLedgerEntry) ID() uuid.UUID              { return e.id }
func (e WalletLedgerEntry) WalletID() uuid.UUID        { return e.walletID }
func (e WalletLedgerEntry) TransactionID() uuid.UUID   { return e.transactionID }
func (e WalletLedgerEntry) Direction() LedgerDirection { return e.direction }
func (e WalletLedgerEntry) Amount() money.Money        { return e.amount }
func (e WalletLedgerEntry) BalanceBefore() money.Money { return e.balanceBefore }
func (e WalletLedgerEntry) BalanceAfter() money.Money  { return e.balanceAfter }
func (e WalletLedgerEntry) CreatedAt() time.Time       { return e.createdAt }
