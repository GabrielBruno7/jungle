package journal_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"jungle/internal/domain/journal"
	"jungle/internal/domain/money"
)

func mustMoney(t *testing.T, s string) money.Money {
	t.Helper()
	m, err := money.Parse(s, money.BRL)
	if err != nil {
		t.Fatalf("money.Parse(%q) unexpected error: %v", s, err)
	}
	return m
}

func TestAccountNames(t *testing.T) {
	walletID := uuid.MustParse("4225f112-adc1-4418-88e9-8f05384b8c24")

	if got := journal.WalletAccount(walletID); got != journal.Account("WALLET:4225f112-adc1-4418-88e9-8f05384b8c24") {
		t.Errorf("WalletAccount() = %q", got)
	}
	if got := journal.ProviderAccount("provider-a"); got != journal.Account("PROVIDER:provider-a") {
		t.Errorf("ProviderAccount() = %q", got)
	}
	if journal.PlatformFunding != journal.Account("PLATFORM_FUNDING") {
		t.Errorf("PlatformFunding = %q", journal.PlatformFunding)
	}
}

func TestNewMovement_ProducesABalancedPair(t *testing.T) {
	transactionID := uuid.New()
	from := journal.WalletAccount(uuid.New())
	to := journal.ProviderAccount("provider-a")
	now := time.Now().UTC()

	entries, err := journal.NewMovement(journal.MovementParams{
		TransactionID: transactionID,
		From:          from,
		To:            to,
		Amount:        mustMoney(t, "80.00"),
		Now:           now,
	})
	if err != nil {
		t.Fatalf("NewMovement unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}

	debit, credit := entries[0], entries[1]

	if debit.Direction() != journal.Debit || debit.Account() != from {
		t.Errorf("first entry = %s on %q, want DEBIT on %q", debit.Direction(), debit.Account(), from)
	}
	if credit.Direction() != journal.Credit || credit.Account() != to {
		t.Errorf("second entry = %s on %q, want CREDIT on %q", credit.Direction(), credit.Account(), to)
	}

	for _, e := range entries {
		if e.TransactionID() != transactionID {
			t.Errorf("TransactionID() = %s, want %s", e.TransactionID(), transactionID)
		}
		if !e.Amount().Equal(mustMoney(t, "80.00")) {
			t.Errorf("Amount() = %s, want 80.00", e.Amount())
		}
		if !e.CreatedAt().Equal(now) {
			t.Errorf("CreatedAt() = %s, want %s", e.CreatedAt(), now)
		}
		if e.ID() == uuid.Nil {
			t.Error("ID() is the nil UUID")
		}
	}
	if debit.ID() == credit.ID() {
		t.Error("both entries share the same ID")
	}
	if err := journal.AssertBalanced(entries); err != nil {
		t.Errorf("AssertBalanced() = %v, want nil", err)
	}
}

func TestNewMovement_Rejects(t *testing.T) {
	account := journal.WalletAccount(uuid.New())
	other := journal.ProviderAccount("provider-a")
	valid := journal.MovementParams{
		TransactionID: uuid.New(),
		From:          account,
		To:            other,
		Amount:        mustMoney(t, "10.00"),
		Now:           time.Now(),
	}

	cases := []struct {
		name   string
		mutate func(p *journal.MovementParams)
		want   error
	}{
		{"no transaction", func(p *journal.MovementParams) { p.TransactionID = uuid.Nil }, journal.ErrInvalidEntry},
		{"no source account", func(p *journal.MovementParams) { p.From = "" }, journal.ErrInvalidAccount},
		{"no destination account", func(p *journal.MovementParams) { p.To = "" }, journal.ErrInvalidAccount},
		{"same account on both sides", func(p *journal.MovementParams) { p.To = p.From }, journal.ErrSameAccount},
		{"zero amount", func(p *journal.MovementParams) { p.Amount = money.Zero(money.BRL) }, journal.ErrInvalidEntry},
		{"negative amount", func(p *journal.MovementParams) { p.Amount = money.FromMinorUnits(-1000, money.BRL) }, journal.ErrInvalidEntry},
		{"no timestamp", func(p *journal.MovementParams) { p.Now = time.Time{} }, journal.ErrInvalidEntry},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := valid
			tc.mutate(&params)

			entries, err := journal.NewMovement(params)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if entries != nil {
				t.Errorf("entries = %v, want nil on rejection", entries)
			}
		})
	}
}

func TestSignedMinorUnits(t *testing.T) {
	entries, err := journal.NewMovement(journal.MovementParams{
		TransactionID: uuid.New(),
		From:          journal.WalletAccount(uuid.New()),
		To:            journal.ProviderAccount("provider-a"),
		Amount:        mustMoney(t, "25.50"),
		Now:           time.Now(),
	})
	if err != nil {
		t.Fatalf("NewMovement unexpected error: %v", err)
	}

	if got := entries[0].SignedMinorUnits(); got != -2550 {
		t.Errorf("debit SignedMinorUnits() = %d, want -2550", got)
	}
	if got := entries[1].SignedMinorUnits(); got != 2550 {
		t.Errorf("credit SignedMinorUnits() = %d, want 2550", got)
	}
}

func TestBalance_SumsEveryEntryAcrossMovements(t *testing.T) {
	var all []journal.Entry
	for _, amount := range []string{"80.00", "20.00", "0.01"} {
		entries, err := journal.NewMovement(journal.MovementParams{
			TransactionID: uuid.New(),
			From:          journal.WalletAccount(uuid.New()),
			To:            journal.ProviderAccount("provider-a"),
			Amount:        mustMoney(t, amount),
			Now:           time.Now(),
		})
		if err != nil {
			t.Fatalf("NewMovement unexpected error: %v", err)
		}
		all = append(all, entries...)
	}

	if got := journal.Balance(all); got != 0 {
		t.Errorf("Balance() = %d, want 0", got)
	}
	if err := journal.AssertBalanced(all); err != nil {
		t.Errorf("AssertBalanced() = %v, want nil", err)
	}
}

func TestAssertBalanced_RejectsAHalfMovement(t *testing.T) {
	entries, err := journal.NewMovement(journal.MovementParams{
		TransactionID: uuid.New(),
		From:          journal.WalletAccount(uuid.New()),
		To:            journal.ProviderAccount("provider-a"),
		Amount:        mustMoney(t, "80.00"),
		Now:           time.Now(),
	})
	if err != nil {
		t.Fatalf("NewMovement unexpected error: %v", err)
	}

	half := entries[:1]

	if got := journal.Balance(half); got != -8000 {
		t.Errorf("Balance() = %d, want -8000", got)
	}
	if err := journal.AssertBalanced(half); !errors.Is(err, journal.ErrNotBalanced) {
		t.Fatalf("error = %v, want ErrNotBalanced", err)
	}
}

func TestAssertBalanced_RejectsMismatchedAmounts(t *testing.T) {
	transactionID := uuid.New()
	account := journal.WalletAccount(uuid.New())
	now := time.Now()

	debit, err := journal.Rehydrate(journal.RehydrateParams{
		ID:            uuid.New(),
		TransactionID: transactionID,
		Account:       account,
		Direction:     journal.Debit,
		Amount:        mustMoney(t, "80.00"),
		CreatedAt:     now,
	})
	if err != nil {
		t.Fatalf("Rehydrate unexpected error: %v", err)
	}
	credit, err := journal.Rehydrate(journal.RehydrateParams{
		ID:            uuid.New(),
		TransactionID: transactionID,
		Account:       journal.ProviderAccount("provider-a"),
		Direction:     journal.Credit,
		Amount:        mustMoney(t, "79.99"),
		CreatedAt:     now,
	})
	if err != nil {
		t.Fatalf("Rehydrate unexpected error: %v", err)
	}

	if err := journal.AssertBalanced([]journal.Entry{debit, credit}); !errors.Is(err, journal.ErrNotBalanced) {
		t.Fatalf("error = %v, want ErrNotBalanced", err)
	}
}

func TestAssertBalanced_EmptyIsBalanced(t *testing.T) {
	if err := journal.AssertBalanced(nil); err != nil {
		t.Errorf("AssertBalanced(nil) = %v, want nil — a LOSS posts no entries", err)
	}
}

func TestRehydrate_KeepsWhatWasStored(t *testing.T) {
	id, transactionID := uuid.New(), uuid.New()
	account := journal.WalletAccount(uuid.New())
	createdAt := time.Date(2026, 9, 17, 23, 45, 0, 0, time.UTC)

	entry, err := journal.Rehydrate(journal.RehydrateParams{
		ID:            id,
		TransactionID: transactionID,
		Account:       account,
		Direction:     journal.Credit,
		Amount:        mustMoney(t, "12.34"),
		CreatedAt:     createdAt,
	})
	if err != nil {
		t.Fatalf("Rehydrate unexpected error: %v", err)
	}

	if entry.ID() != id || entry.TransactionID() != transactionID || entry.Account() != account {
		t.Errorf("identity not preserved: %s / %s / %q", entry.ID(), entry.TransactionID(), entry.Account())
	}
	if entry.Direction() != journal.Credit || !entry.Amount().Equal(mustMoney(t, "12.34")) {
		t.Errorf("movement not preserved: %s %s", entry.Direction(), entry.Amount())
	}
	if !entry.CreatedAt().Equal(createdAt) {
		t.Errorf("CreatedAt() = %s, want %s", entry.CreatedAt(), createdAt)
	}
}

func TestRehydrate_RejectsAnUnknownDirection(t *testing.T) {
	_, err := journal.Rehydrate(journal.RehydrateParams{
		ID:            uuid.New(),
		TransactionID: uuid.New(),
		Account:       journal.WalletAccount(uuid.New()),
		Direction:     journal.Direction("TRANSFER"),
		Amount:        mustMoney(t, "10.00"),
		CreatedAt:     time.Now(),
	})
	if !errors.Is(err, journal.ErrInvalidEntry) {
		t.Fatalf("error = %v, want ErrInvalidEntry", err)
	}
}
