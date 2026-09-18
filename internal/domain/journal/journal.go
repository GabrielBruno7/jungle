package journal

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"jungle/internal/domain/money"
)

var (
	ErrInvalidEntry   = errors.New("journal: invalid entry")
	ErrSameAccount    = errors.New("journal: a movement must have two different accounts")
	ErrNotBalanced    = errors.New("journal: entries do not balance")
	ErrInvalidAccount = errors.New("journal: invalid account")
)

type Account string

const PlatformFunding Account = "PLATFORM_FUNDING"

func WalletAccount(walletID uuid.UUID) Account {
	return Account("WALLET:" + walletID.String())
}

func ProviderAccount(providerID string) Account {
	return Account("PROVIDER:" + providerID)
}

type Direction string

const (
	Debit  Direction = "DEBIT"
	Credit Direction = "CREDIT"
)

type Entry struct {
	id            uuid.UUID
	transactionID uuid.UUID
	account       Account
	direction     Direction
	amount        money.Money
	createdAt     time.Time
}

type MovementParams struct {
	TransactionID uuid.UUID
	From          Account
	To            Account
	Amount        money.Money
	Now           time.Time
}

func NewMovement(p MovementParams) ([]Entry, error) {
	if p.TransactionID == uuid.Nil {
		return nil, fmt.Errorf("%w: transactionId is required", ErrInvalidEntry)
	}
	if p.From == "" || p.To == "" {
		return nil, fmt.Errorf("%w: both accounts are required", ErrInvalidAccount)
	}
	if p.From == p.To {
		return nil, fmt.Errorf("%w: %s", ErrSameAccount, p.From)
	}
	if !p.Amount.IsPositive() {
		return nil, fmt.Errorf("%w: amount must be positive", ErrInvalidEntry)
	}
	if p.Now.IsZero() {
		return nil, fmt.Errorf("%w: now is required", ErrInvalidEntry)
	}

	return []Entry{
		{
			id:            uuid.New(),
			transactionID: p.TransactionID,
			account:       p.From,
			direction:     Debit,
			amount:        p.Amount,
			createdAt:     p.Now,
		},
		{
			id:            uuid.New(),
			transactionID: p.TransactionID,
			account:       p.To,
			direction:     Credit,
			amount:        p.Amount,
			createdAt:     p.Now,
		},
	}, nil
}

type RehydrateParams struct {
	ID            uuid.UUID
	TransactionID uuid.UUID
	Account       Account
	Direction     Direction
	Amount        money.Money
	CreatedAt     time.Time
}

func Rehydrate(p RehydrateParams) (Entry, error) {
	if p.Direction != Debit && p.Direction != Credit {
		return Entry{}, fmt.Errorf("%w: direction %q", ErrInvalidEntry, p.Direction)
	}
	return Entry{
		id:            p.ID,
		transactionID: p.TransactionID,
		account:       p.Account,
		direction:     p.Direction,
		amount:        p.Amount,
		createdAt:     p.CreatedAt,
	}, nil
}

func (e Entry) ID() uuid.UUID            { return e.id }
func (e Entry) TransactionID() uuid.UUID { return e.transactionID }
func (e Entry) Account() Account         { return e.account }
func (e Entry) Direction() Direction     { return e.direction }
func (e Entry) Amount() money.Money      { return e.amount }
func (e Entry) CreatedAt() time.Time     { return e.createdAt }

func (e Entry) SignedMinorUnits() int64 {
	if e.direction == Debit {
		return -e.amount.MinorUnits()
	}
	return e.amount.MinorUnits()
}

func Balance(entries []Entry) int64 {
	var total int64
	for _, e := range entries {
		total += e.SignedMinorUnits()
	}
	return total
}

func AssertBalanced(entries []Entry) error {
	if total := Balance(entries); total != 0 {
		return fmt.Errorf("%w: off by %d minor units", ErrNotBalanced, total)
	}
	return nil
}
