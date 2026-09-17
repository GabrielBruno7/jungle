// Package wallet holds the financial aggregate of the domain: Wallet (the
// aggregate root) and WalletLedgerEntry (its append-only movement history).
// Like internal/domain/money, this package has no dependency on Fx, HTTP,
// SQL, or any other infrastructure package.
package wallet

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"jungle/internal/domain/money"
)

var (
	ErrCurrencyMismatch    = errors.New("wallet: movement currency does not match wallet currency")
	ErrAmountNotPositive   = errors.New("wallet: amount must be positive")
	ErrInsufficientBalance = errors.New("wallet: insufficient balance")
	ErrInvalidWallet       = errors.New("wallet: invalid wallet state")
)

// Wallet is the aggregate root of the financial domain: it owns the
// player's balance in a single currency, and every rule that must hold for
// that balance is enforced here, regardless of whether the caller is the
// HTTP handler or the SQS consumer.
type Wallet struct {
	id        uuid.UUID
	playerID  uuid.UUID
	currency  money.Currency
	balance   money.Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

// NewWalletParams carries the arguments to open a brand-new wallet.
type NewWalletParams struct {
	ID             uuid.UUID
	PlayerID       uuid.UUID
	Currency       money.Currency
	InitialBalance money.Money
	Now            time.Time
}

// NewWallet opens a new wallet. InitialBalance must be zero or positive —
// a wallet can never be opened already in debt — and must already be
// denominated in Currency. The wallet always starts at version 1,
// regardless of InitialBalance.
//
// NewWallet does not decide whether an OPENING WagerTransaction or a
// WalletLedgerEntry should also be recorded for a positive initial
// balance — that spans more than this one aggregate, so it belongs to the
// OpenWallet use case, not to Wallet itself.
func NewWallet(p NewWalletParams) (Wallet, error) {
	if p.ID == uuid.Nil {
		return Wallet{}, fmt.Errorf("%w: id is required", ErrInvalidWallet)
	}
	if p.PlayerID == uuid.Nil {
		return Wallet{}, fmt.Errorf("%w: playerId is required", ErrInvalidWallet)
	}
	if p.Currency == "" {
		return Wallet{}, fmt.Errorf("%w: currency is required", ErrInvalidWallet)
	}
	if p.InitialBalance.Currency() != p.Currency {
		return Wallet{}, ErrCurrencyMismatch
	}
	if p.InitialBalance.IsNegative() {
		return Wallet{}, fmt.Errorf("%w: initial balance cannot be negative", ErrInvalidWallet)
	}
	if p.Now.IsZero() {
		return Wallet{}, fmt.Errorf("%w: now is required", ErrInvalidWallet)
	}

	return Wallet{
		id:        p.ID,
		playerID:  p.PlayerID,
		currency:  p.Currency,
		balance:   p.InitialBalance,
		version:   1,
		createdAt: p.Now,
		updatedAt: p.Now,
	}, nil
}

// RehydrateWalletParams carries the exact persisted state of a wallet.
type RehydrateWalletParams struct {
	ID        uuid.UUID
	PlayerID  uuid.UUID
	Currency  money.Currency
	Balance   money.Money
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// RehydrateWallet reconstructs a Wallet from already-persisted state. It
// validates structural invariants (matching currency, non-negative
// balance, version >= 1) but never mutates anything or re-derives balance
// or version from history — that would replay movements that were already
// applied and committed.
func RehydrateWallet(p RehydrateWalletParams) (Wallet, error) {
	if p.ID == uuid.Nil || p.PlayerID == uuid.Nil || p.Currency == "" {
		return Wallet{}, fmt.Errorf("%w: missing identity fields", ErrInvalidWallet)
	}
	if p.Balance.Currency() != p.Currency {
		return Wallet{}, ErrCurrencyMismatch
	}
	if p.Balance.IsNegative() {
		return Wallet{}, fmt.Errorf("%w: persisted balance is negative", ErrInvalidWallet)
	}
	if p.Version < 1 {
		return Wallet{}, fmt.Errorf("%w: version must be >= 1", ErrInvalidWallet)
	}

	return Wallet{
		id:        p.ID,
		playerID:  p.PlayerID,
		currency:  p.Currency,
		balance:   p.Balance,
		version:   p.Version,
		createdAt: p.CreatedAt,
		updatedAt: p.UpdatedAt,
	}, nil
}

func (w Wallet) ID() uuid.UUID            { return w.id }
func (w Wallet) PlayerID() uuid.UUID      { return w.playerID }
func (w Wallet) Currency() money.Currency { return w.currency }
func (w Wallet) Balance() money.Money     { return w.balance }
func (w Wallet) Version() int64           { return w.version }
func (w Wallet) CreatedAt() time.Time     { return w.createdAt }
func (w Wallet) UpdatedAt() time.Time     { return w.updatedAt }

// Debit subtracts amount from the wallet's balance. amount must be
// positive and share the wallet's currency, and the resulting balance must
// remain >= 0 — the central invariant this aggregate exists to protect.
// On success the wallet's version is incremented and updatedAt set to now;
// on failure the wallet is left completely unchanged.
func (w *Wallet) Debit(amount money.Money, now time.Time) error {
	if amount.Currency() != w.currency {
		return ErrCurrencyMismatch
	}
	if !amount.IsPositive() {
		return ErrAmountNotPositive
	}

	newBalance, err := w.balance.Sub(amount)
	if err != nil {
		return err
	}
	if newBalance.IsNegative() {
		return ErrInsufficientBalance
	}

	w.balance = newBalance
	w.version++
	w.updatedAt = now
	return nil
}

// Credit adds amount to the wallet's balance. amount must be positive and
// share the wallet's currency. On success the wallet's version is
// incremented and updatedAt set to now; on failure the wallet is left
// completely unchanged.
func (w *Wallet) Credit(amount money.Money, now time.Time) error {
	if amount.Currency() != w.currency {
		return ErrCurrencyMismatch
	}
	if !amount.IsPositive() {
		return ErrAmountNotPositive
	}

	newBalance, err := w.balance.Add(amount)
	if err != nil {
		return err
	}

	w.balance = newBalance
	w.version++
	w.updatedAt = now
	return nil
}
