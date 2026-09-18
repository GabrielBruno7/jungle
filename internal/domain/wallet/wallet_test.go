package wallet_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"jungle/internal/domain/money"
	"jungle/internal/domain/wallet"
)

func mustMoney(t *testing.T, s string) money.Money {
	t.Helper()
	m, err := money.Parse(s, money.BRL)
	if err != nil {
		t.Fatalf("money.Parse(%q) unexpected error: %v", s, err)
	}
	return m
}

func newTestWallet(t *testing.T, initial string) wallet.Wallet {
	t.Helper()
	w, err := wallet.NewWallet(wallet.NewWalletParams{
		ID:             uuid.New(),
		PlayerID:       uuid.New(),
		Currency:       money.BRL,
		InitialBalance: mustMoney(t, initial),
		Now:            time.Now(),
	})
	if err != nil {
		t.Fatalf("NewWallet unexpected error: %v", err)
	}
	return w
}

func TestNewWallet_Valid(t *testing.T) {
	w := newTestWallet(t, "1000.00")

	if w.Version() != 1 {
		t.Errorf("Version() = %d, want 1", w.Version())
	}
	if !w.Balance().Equal(mustMoney(t, "1000.00")) {
		t.Errorf("Balance() = %s, want 1000.00", w.Balance())
	}
	if w.CreatedAt() != w.UpdatedAt() {
		t.Errorf("CreatedAt/UpdatedAt should match on a fresh wallet")
	}
}

func TestNewWallet_ZeroBalance(t *testing.T) {
	w := newTestWallet(t, "0.00")
	if !w.Balance().IsZero() {
		t.Errorf("Balance() = %s, want 0.00", w.Balance())
	}
	if w.Version() != 1 {
		t.Errorf("Version() = %d, want 1", w.Version())
	}
}

func TestNewWallet_RejectsNegativeInitialBalance(t *testing.T) {
	_, err := wallet.NewWallet(wallet.NewWalletParams{
		ID:             uuid.New(),
		PlayerID:       uuid.New(),
		Currency:       money.BRL,
		InitialBalance: mustMoney(t, "-1.00"),
		Now:            time.Now(),
	})
	if !errors.Is(err, wallet.ErrInvalidWallet) {
		t.Fatalf("error = %v, want ErrInvalidWallet", err)
	}
}

func TestNewWallet_RejectsCurrencyMismatch(t *testing.T) {
	usd, err := money.NewCurrency("USD")
	if err != nil {
		t.Fatalf("NewCurrency unexpected error: %v", err)
	}
	usdAmount, err := money.Parse("10.00", usd)
	if err != nil {
		t.Fatalf("Parse unexpected error: %v", err)
	}

	_, err = wallet.NewWallet(wallet.NewWalletParams{
		ID:             uuid.New(),
		PlayerID:       uuid.New(),
		Currency:       money.BRL,
		InitialBalance: usdAmount,
		Now:            time.Now(),
	})
	if !errors.Is(err, wallet.ErrCurrencyMismatch) {
		t.Fatalf("error = %v, want ErrCurrencyMismatch", err)
	}
}

func TestNewWallet_RejectsMissingIdentity(t *testing.T) {
	base := wallet.NewWalletParams{
		ID:             uuid.New(),
		PlayerID:       uuid.New(),
		Currency:       money.BRL,
		InitialBalance: mustMoney(t, "0.00"),
		Now:            time.Now(),
	}

	withoutID := base
	withoutID.ID = uuid.Nil
	if _, err := wallet.NewWallet(withoutID); !errors.Is(err, wallet.ErrInvalidWallet) {
		t.Errorf("missing ID: error = %v, want ErrInvalidWallet", err)
	}

	withoutPlayer := base
	withoutPlayer.PlayerID = uuid.Nil
	if _, err := wallet.NewWallet(withoutPlayer); !errors.Is(err, wallet.ErrInvalidWallet) {
		t.Errorf("missing PlayerID: error = %v, want ErrInvalidWallet", err)
	}
}

func TestDebit_Success(t *testing.T) {
	w := newTestWallet(t, "100.00")
	now := time.Now()

	if err := w.Debit(mustMoney(t, "80.00"), now); err != nil {
		t.Fatalf("Debit unexpected error: %v", err)
	}
	if !w.Balance().Equal(mustMoney(t, "20.00")) {
		t.Errorf("Balance() = %s, want 20.00", w.Balance())
	}
	if w.Version() != 2 {
		t.Errorf("Version() = %d, want 2", w.Version())
	}
	if !w.UpdatedAt().Equal(now) {
		t.Errorf("UpdatedAt() not updated to now")
	}
}

func TestDebit_ExactBalance_LeavesZero(t *testing.T) {
	w := newTestWallet(t, "50.00")
	if err := w.Debit(mustMoney(t, "50.00"), time.Now()); err != nil {
		t.Fatalf("Debit unexpected error: %v", err)
	}
	if !w.Balance().IsZero() {
		t.Errorf("Balance() = %s, want 0.00", w.Balance())
	}
}

func TestDebit_InsufficientBalance_LeavesWalletUnchanged(t *testing.T) {
	w := newTestWallet(t, "100.00")

	err := w.Debit(mustMoney(t, "100.01"), time.Now())
	if !errors.Is(err, wallet.ErrInsufficientBalance) {
		t.Fatalf("error = %v, want ErrInsufficientBalance", err)
	}
	if !w.Balance().Equal(mustMoney(t, "100.00")) {
		t.Errorf("Balance() = %s, want unchanged 100.00", w.Balance())
	}
	if w.Version() != 1 {
		t.Errorf("Version() = %d, want unchanged 1", w.Version())
	}
}

func TestDebit_TwoConcurrentBets(t *testing.T) {
	w := newTestWallet(t, "100.00")
	bet := mustMoney(t, "80.00")

	err1 := w.Debit(bet, time.Now())
	err2 := w.Debit(bet, time.Now())

	successes, failures := 0, 0
	for _, err := range []error{err1, err2} {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, wallet.ErrInsufficientBalance):
			failures++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if successes != 1 || failures != 1 {
		t.Fatalf("got %d successes and %d failures, want exactly 1 and 1", successes, failures)
	}
	if !w.Balance().Equal(mustMoney(t, "20.00")) {
		t.Errorf("Balance() = %s, want 20.00", w.Balance())
	}
	if w.Version() != 2 {
		t.Errorf("Version() = %d, want 2 (only one successful movement)", w.Version())
	}
}

func TestDebit_RejectsNonPositiveAmount(t *testing.T) {
	w := newTestWallet(t, "100.00")
	if err := w.Debit(mustMoney(t, "0.00"), time.Now()); !errors.Is(err, wallet.ErrAmountNotPositive) {
		t.Errorf("Debit(0.00) error = %v, want ErrAmountNotPositive", err)
	}
	if err := w.Debit(mustMoney(t, "-10.00"), time.Now()); !errors.Is(err, wallet.ErrAmountNotPositive) {
		t.Errorf("Debit(-10.00) error = %v, want ErrAmountNotPositive", err)
	}
}

func TestDebit_RejectsCurrencyMismatch(t *testing.T) {
	w := newTestWallet(t, "100.00")
	usd, err := money.NewCurrency("USD")
	if err != nil {
		t.Fatalf("NewCurrency unexpected error: %v", err)
	}
	usdAmount, err := money.Parse("10.00", usd)
	if err != nil {
		t.Fatalf("Parse unexpected error: %v", err)
	}

	if err := w.Debit(usdAmount, time.Now()); !errors.Is(err, wallet.ErrCurrencyMismatch) {
		t.Errorf("error = %v, want ErrCurrencyMismatch", err)
	}
}

func TestCredit_Success(t *testing.T) {
	w := newTestWallet(t, "100.00")
	now := time.Now()

	if err := w.Credit(mustMoney(t, "50.00"), now); err != nil {
		t.Fatalf("Credit unexpected error: %v", err)
	}
	if !w.Balance().Equal(mustMoney(t, "150.00")) {
		t.Errorf("Balance() = %s, want 150.00", w.Balance())
	}
	if w.Version() != 2 {
		t.Errorf("Version() = %d, want 2", w.Version())
	}
}

func TestCredit_RejectsNonPositiveAmount(t *testing.T) {
	w := newTestWallet(t, "100.00")
	if err := w.Credit(mustMoney(t, "0.00"), time.Now()); !errors.Is(err, wallet.ErrAmountNotPositive) {
		t.Errorf("Credit(0.00) error = %v, want ErrAmountNotPositive", err)
	}
}

func TestRehydrateWallet_Valid(t *testing.T) {
	now := time.Now()
	w, err := wallet.RehydrateWallet(wallet.RehydrateWalletParams{
		ID:        uuid.New(),
		PlayerID:  uuid.New(),
		Currency:  money.BRL,
		Balance:   mustMoney(t, "42.00"),
		Version:   7,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("RehydrateWallet unexpected error: %v", err)
	}
	if w.Version() != 7 {
		t.Errorf("Version() = %d, want 7 (rehydration must not reset it)", w.Version())
	}
}

func TestRehydrateWallet_RejectsNegativeBalance(t *testing.T) {
	_, err := wallet.RehydrateWallet(wallet.RehydrateWalletParams{
		ID:        uuid.New(),
		PlayerID:  uuid.New(),
		Currency:  money.BRL,
		Balance:   mustMoney(t, "-1.00"),
		Version:   1,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	if !errors.Is(err, wallet.ErrInvalidWallet) {
		t.Fatalf("error = %v, want ErrInvalidWallet", err)
	}
}

func TestRehydrateWallet_RejectsVersionBelowOne(t *testing.T) {
	_, err := wallet.RehydrateWallet(wallet.RehydrateWalletParams{
		ID:        uuid.New(),
		PlayerID:  uuid.New(),
		Currency:  money.BRL,
		Balance:   mustMoney(t, "0.00"),
		Version:   0,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	if !errors.Is(err, wallet.ErrInvalidWallet) {
		t.Fatalf("error = %v, want ErrInvalidWallet", err)
	}
}
