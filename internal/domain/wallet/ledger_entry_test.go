package wallet_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"jungle/internal/domain/wallet"
)

func TestNewWalletLedgerEntry_Debit(t *testing.T) {
	entry, err := wallet.NewWalletLedgerEntry(wallet.NewWalletLedgerEntryParams{
		ID:            uuid.New(),
		WalletID:      uuid.New(),
		TransactionID: uuid.New(),
		Direction:     wallet.Debit,
		Amount:        mustMoney(t, "80.00"),
		BalanceBefore: mustMoney(t, "100.00"),
		Now:           time.Now(),
	})
	if err != nil {
		t.Fatalf("NewWalletLedgerEntry unexpected error: %v", err)
	}
	if !entry.BalanceAfter().Equal(mustMoney(t, "20.00")) {
		t.Errorf("BalanceAfter() = %s, want 20.00", entry.BalanceAfter())
	}
}

func TestNewWalletLedgerEntry_Credit(t *testing.T) {
	entry, err := wallet.NewWalletLedgerEntry(wallet.NewWalletLedgerEntryParams{
		ID:            uuid.New(),
		WalletID:      uuid.New(),
		TransactionID: uuid.New(),
		Direction:     wallet.Credit,
		Amount:        mustMoney(t, "50.00"),
		BalanceBefore: mustMoney(t, "100.00"),
		Now:           time.Now(),
	})
	if err != nil {
		t.Fatalf("NewWalletLedgerEntry unexpected error: %v", err)
	}
	if !entry.BalanceAfter().Equal(mustMoney(t, "150.00")) {
		t.Errorf("BalanceAfter() = %s, want 150.00", entry.BalanceAfter())
	}
}

func TestNewWalletLedgerEntry_RejectsNonPositiveAmount(t *testing.T) {
	_, err := wallet.NewWalletLedgerEntry(wallet.NewWalletLedgerEntryParams{
		ID:            uuid.New(),
		WalletID:      uuid.New(),
		TransactionID: uuid.New(),
		Direction:     wallet.Credit,
		Amount:        mustMoney(t, "0.00"),
		BalanceBefore: mustMoney(t, "100.00"),
		Now:           time.Now(),
	})
	if !errors.Is(err, wallet.ErrInvalidLedgerEntry) {
		t.Fatalf("error = %v, want ErrInvalidLedgerEntry", err)
	}
}

func TestNewWalletLedgerEntry_RejectsInvalidDirection(t *testing.T) {
	_, err := wallet.NewWalletLedgerEntry(wallet.NewWalletLedgerEntryParams{
		ID:            uuid.New(),
		WalletID:      uuid.New(),
		TransactionID: uuid.New(),
		Direction:     wallet.LedgerDirection("SIDEWAYS"),
		Amount:        mustMoney(t, "10.00"),
		BalanceBefore: mustMoney(t, "100.00"),
		Now:           time.Now(),
	})
	if !errors.Is(err, wallet.ErrInvalidDirection) {
		t.Fatalf("error = %v, want ErrInvalidDirection", err)
	}
}

func TestNewWalletLedgerEntry_DebitBelowZero_Rejected(t *testing.T) {
	_, err := wallet.NewWalletLedgerEntry(wallet.NewWalletLedgerEntryParams{
		ID:            uuid.New(),
		WalletID:      uuid.New(),
		TransactionID: uuid.New(),
		Direction:     wallet.Debit,
		Amount:        mustMoney(t, "150.00"),
		BalanceBefore: mustMoney(t, "100.00"),
		Now:           time.Now(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRehydrateWalletLedgerEntry_Valid(t *testing.T) {
	entry, err := wallet.RehydrateWalletLedgerEntry(wallet.RehydrateWalletLedgerEntryParams{
		ID:            uuid.New(),
		WalletID:      uuid.New(),
		TransactionID: uuid.New(),
		Direction:     wallet.Debit,
		Amount:        mustMoney(t, "80.00"),
		BalanceBefore: mustMoney(t, "100.00"),
		BalanceAfter:  mustMoney(t, "20.00"),
		CreatedAt:     time.Now(),
	})
	if err != nil {
		t.Fatalf("RehydrateWalletLedgerEntry unexpected error: %v", err)
	}
	if !entry.BalanceAfter().Equal(mustMoney(t, "20.00")) {
		t.Errorf("BalanceAfter() = %s, want 20.00", entry.BalanceAfter())
	}
}

func TestRehydrateWalletLedgerEntry_RejectsCorruptBalanceAfter(t *testing.T) {
	_, err := wallet.RehydrateWalletLedgerEntry(wallet.RehydrateWalletLedgerEntryParams{
		ID:            uuid.New(),
		WalletID:      uuid.New(),
		TransactionID: uuid.New(),
		Direction:     wallet.Debit,
		Amount:        mustMoney(t, "80.00"),
		BalanceBefore: mustMoney(t, "100.00"),
		BalanceAfter:  mustMoney(t, "50.00"),
		CreatedAt:     time.Now(),
	})
	if !errors.Is(err, wallet.ErrLedgerInvariant) {
		t.Fatalf("error = %v, want ErrLedgerInvariant", err)
	}
}
