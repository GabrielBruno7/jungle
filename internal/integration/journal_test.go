//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"jungle/internal/domain/journal"
	"jungle/internal/domain/wagertx"
)

func (i *instance) journalImbalance(t *testing.T, transactionID uuid.UUID) int64 {
	t.Helper()

	var imbalance int64
	err := i.pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(
			CASE WHEN direction = 'CREDIT' THEN amount_minor_units ELSE -amount_minor_units END
		), 0)
		FROM journal_entry WHERE transaction_id = $1`, transactionID).Scan(&imbalance)
	if err != nil {
		t.Fatalf("computing imbalance: %v", err)
	}
	return imbalance
}

func (i *instance) journalEntries(t *testing.T, transactionID uuid.UUID) []journal.Entry {
	t.Helper()

	entries, err := i.repos.Journal().ByTransaction(context.Background(), transactionID)
	if err != nil {
		t.Fatalf("reading journal entries: %v", err)
	}
	return entries
}

func TestJournal_EveryMovementProducesABalancedPair(t *testing.T) {
	i := newInstance(t, "test")
	walletID, playerID := i.openWallet(t, "1000.00")
	suffix := uuid.NewString()[:8]
	betID := "j-bet-" + suffix

	cases := []struct {
		name          string
		op            operation
		walletSide    journal.Direction
		counterparty  journal.Account
		amountInCents int64
	}{
		{
			name:          "BET moves money from the wallet to the provider",
			op:            operation{externalID: betID, playerID: playerID, walletID: walletID, kind: wagertx.Bet, amount: "25.00"},
			walletSide:    journal.Debit,
			counterparty:  journal.ProviderAccount("provider-a"),
			amountInCents: 2500,
		},
		{
			name:          "WIN moves money from the provider to the wallet",
			op:            operation{externalID: "j-win-" + suffix, playerID: playerID, walletID: walletID, kind: wagertx.Win, amount: "50.00"},
			walletSide:    journal.Credit,
			counterparty:  journal.ProviderAccount("provider-a"),
			amountInCents: 5000,
		},
		{
			name: "REFUND gives the stake back to the wallet",
			op: operation{externalID: "j-refund-" + suffix, playerID: playerID, walletID: walletID,
				kind: wagertx.Refund, amount: "25.00", reference: betID},
			walletSide:    journal.Credit,
			counterparty:  journal.ProviderAccount("provider-a"),
			amountInCents: 2500,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := i.mustSubmit(t, tc.op)
			if result.Transaction.Status() != wagertx.Processed {
				t.Fatalf("status = %s, want PROCESSED", result.Transaction.Status())
			}

			entries := i.journalEntries(t, result.Transaction.ID())
			if len(entries) != 2 {
				t.Fatalf("got %d journal entries, want exactly 2", len(entries))
			}
			if imbalance := i.journalImbalance(t, result.Transaction.ID()); imbalance != 0 {
				t.Errorf("entries are off by %d minor units", imbalance)
			}

			walletAccount := journal.WalletAccount(walletID)
			for _, e := range entries {
				switch e.Account() {
				case walletAccount:
					if e.Direction() != tc.walletSide {
						t.Errorf("wallet side = %s, want %s", e.Direction(), tc.walletSide)
					}
				case tc.counterparty:
					if e.Direction() == tc.walletSide {
						t.Errorf("counterparty moved the same way as the wallet; the pair does not offset")
					}
				default:
					t.Errorf("unexpected account %s", e.Account())
				}
				if e.Amount().MinorUnits() != tc.amountInCents {
					t.Errorf("amount = %d, want %d", e.Amount().MinorUnits(), tc.amountInCents)
				}
			}
		})
	}
}

func TestJournal_LossProducesNoEntries(t *testing.T) {
	i := newInstance(t, "test")
	walletID, playerID := i.openWallet(t, "100.00")
	suffix := uuid.NewString()[:8]

	result := i.mustSubmit(t, operation{externalID: "j-loss-" + suffix, playerID: playerID,
		walletID: walletID, kind: wagertx.Loss, amount: "0.00"})

	if entries := i.journalEntries(t, result.Transaction.ID()); len(entries) != 0 {
		t.Errorf("got %d journal entries for a LOSS, want 0 — it moves no money", len(entries))
	}
}

func TestJournal_RejectedOperationProducesNoEntries(t *testing.T) {
	i := newInstance(t, "test")
	walletID, playerID := i.openWallet(t, "10.00")
	suffix := uuid.NewString()[:8]

	result := i.mustSubmit(t, operation{externalID: "j-broke-" + suffix, playerID: playerID,
		walletID: walletID, kind: wagertx.Bet, amount: "9999.00"})

	if result.Transaction.Status() != wagertx.Rejected {
		t.Fatalf("status = %s, want REJECTED", result.Transaction.Status())
	}
	if entries := i.journalEntries(t, result.Transaction.ID()); len(entries) != 0 {
		t.Errorf("got %d journal entries for a rejected operation, want 0", len(entries))
	}
}

func TestJournal_WholeSystemBalancesToZero(t *testing.T) {
	i := newInstance(t, "test")
	walletID, playerID := i.openWallet(t, "500.00")
	suffix := uuid.NewString()[:8]

	for n, kind := range []wagertx.Kind{wagertx.Bet, wagertx.Win, wagertx.Bet, wagertx.Loss} {
		amount := "10.00"
		if kind == wagertx.Loss {
			amount = "0.00"
		}
		i.mustSubmit(t, operation{
			externalID: fmt.Sprintf("j-sys-%s-%d", suffix, n),
			playerID:   playerID, walletID: walletID, kind: kind, amount: amount,
		})
	}

	imbalance, err := i.repos.Journal().GlobalImbalance(context.Background())
	if err != nil {
		t.Fatalf("computing global imbalance: %v", err)
	}
	if imbalance != 0 {
		t.Errorf("the whole journal is off by %d minor units; money was created or destroyed", imbalance)
	}
}

func TestJournal_WalletAccountMatchesTheWalletBalance(t *testing.T) {
	i := newInstance(t, "test")
	walletID, playerID := i.openWallet(t, "1000.00")
	suffix := uuid.NewString()[:8]

	i.mustSubmit(t, operation{externalID: "j-m1-" + suffix, playerID: playerID, walletID: walletID, kind: wagertx.Bet, amount: "25.00"})
	i.mustSubmit(t, operation{externalID: "j-m2-" + suffix, playerID: playerID, walletID: walletID, kind: wagertx.Win, amount: "10.00"})

	fromJournal, err := i.repos.Journal().AccountBalance(context.Background(), journal.WalletAccount(walletID))
	if err != nil {
		t.Fatalf("computing account balance: %v", err)
	}

	w, err := i.queries.GetWallet(context.Background(), internalIdentity, walletID)
	if err != nil {
		t.Fatalf("reading wallet: %v", err)
	}

	if fromJournal != w.Balance().MinorUnits() {
		t.Errorf("the journal says %d minor units and the wallet says %d; the two views disagree",
			fromJournal, w.Balance().MinorUnits())
	}
}

func TestJournal_DatabaseRefusesAnUnbalancedPair(t *testing.T) {
	i := newInstance(t, "test")
	walletID, playerID := i.openWallet(t, "100.00")
	suffix := uuid.NewString()[:8]

	result := i.mustSubmit(t, operation{externalID: "j-unbal-" + suffix, playerID: playerID,
		walletID: walletID, kind: wagertx.Bet, amount: "10.00"})

	ctx := context.Background()
	tx, err := i.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("beginning transaction: %v", err)
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO journal_entry (id, transaction_id, account, direction, amount_minor_units, currency)
		VALUES ($1, $2, 'WALLET:orphan', 'CREDIT', 1234, 'BRL')`,
		uuid.New(), result.Transaction.ID())
	if err != nil {
		t.Fatalf("the insert itself should succeed; the deferred trigger fires at commit: %v", err)
	}

	if err := tx.Commit(ctx); err == nil {
		t.Error("the database committed an unbalanced journal; the deferred constraint did not fire")
	}
}

func TestJournal_IsAppendOnly(t *testing.T) {
	i := newInstance(t, "test")
	walletID, playerID := i.openWallet(t, "100.00")
	suffix := uuid.NewString()[:8]

	result := i.mustSubmit(t, operation{externalID: "j-immut-" + suffix, playerID: playerID,
		walletID: walletID, kind: wagertx.Bet, amount: "10.00"})

	ctx := context.Background()
	if _, err := i.pool.Exec(ctx,
		`UPDATE journal_entry SET amount_minor_units = 1 WHERE transaction_id = $1`,
		result.Transaction.ID()); err == nil {
		t.Error("the database allowed a journal entry to be updated")
	}
	if _, err := i.pool.Exec(ctx,
		`DELETE FROM journal_entry WHERE transaction_id = $1`,
		result.Transaction.ID()); err == nil {
		t.Error("the database allowed a journal entry to be deleted")
	}
}
