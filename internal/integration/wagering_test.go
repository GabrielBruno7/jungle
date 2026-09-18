//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"jungle/internal/app"
	"jungle/internal/domain/wagertx"
)

func TestOpenWallet_FundedOpeningIsAtomic(t *testing.T) {
	i := newInstance(t, "test")
	walletID, _ := i.openWallet(t, "1000.00")

	if got := i.balance(t, walletID); got != "1000.00" {
		t.Errorf("balance = %s, want 1000.00", got)
	}
	if got := i.ledgerCount(t, walletID); got != 1 {
		t.Errorf("ledger entries = %d, want 1 (the opening credit)", got)
	}

	var kind, status string
	err := i.pool.QueryRow(context.Background(),
		`SELECT kind, status FROM wager_transaction WHERE wallet_id = $1`, walletID).Scan(&kind, &status)
	if err != nil {
		t.Fatalf("reading opening transaction: %v", err)
	}
	if kind != "OPENING" || status != "PROCESSED" {
		t.Errorf("opening transaction = %s/%s, want OPENING/PROCESSED", kind, status)
	}

	var events int
	if err := i.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM outbox WHERE aggregate_id = $1 OR aggregate_id IN
		 (SELECT id::text FROM wager_transaction WHERE wallet_id = $2)`,
		walletID.String(), walletID).Scan(&events); err != nil {
		t.Fatalf("counting outbox events: %v", err)
	}
	if events != 2 {
		t.Errorf("outbox events = %d, want 2 (WagerTransactionProcessed + WalletBalanceChanged)", events)
	}
}

func TestOpenWallet_ZeroBalanceCreatesNothingExtra(t *testing.T) {
	i := newInstance(t, "test")
	walletID, _ := i.openWallet(t, "0.00")

	if got := i.ledgerCount(t, walletID); got != 0 {
		t.Errorf("ledger entries = %d, want 0", got)
	}

	var transactions int
	if err := i.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM wager_transaction WHERE wallet_id = $1`, walletID).Scan(&transactions); err != nil {
		t.Fatalf("counting transactions: %v", err)
	}
	if transactions != 0 {
		t.Errorf("transactions = %d, want 0", transactions)
	}
}

func TestOpenWallet_DuplicateIsRejectedByTheDatabase(t *testing.T) {
	i := newInstance(t, "test")
	playerID := uuid.New()

	cmd := app.OpenWalletCommand{
		PlayerID:       playerID,
		InitialBalance: brl(t, "10.00"),
		CorrelationID:  uuid.New(),
	}
	if _, err := i.open.Execute(context.Background(), cmd); err != nil {
		t.Fatalf("first opening: %v", err)
	}

	cmd.CorrelationID = uuid.New()
	_, err := i.open.Execute(context.Background(), cmd)
	if !errors.Is(err, app.ErrWalletAlreadyExists) {
		t.Fatalf("second opening error = %v, want ErrWalletAlreadyExists", err)
	}
}

func TestOperationMatrix(t *testing.T) {
	i := newInstance(t, "test")
	walletID, playerID := i.openWallet(t, "1000.00")
	suffix := uuid.NewString()[:8]

	steps := []struct {
		op          operation
		wantStatus  wagertx.Status
		wantBalance string
	}{
		{operation{externalID: "bet-" + suffix, playerID: playerID, walletID: walletID, kind: wagertx.Bet, amount: "25.00"},
			wagertx.Processed, "975.00"},
		{operation{externalID: "win-" + suffix, playerID: playerID, walletID: walletID, kind: wagertx.Win, amount: "50.00"},
			wagertx.Processed, "1025.00"},
		{operation{externalID: "loss-" + suffix, playerID: playerID, walletID: walletID, kind: wagertx.Loss, amount: "0.00"},
			wagertx.Processed, "1025.00"},
		{operation{externalID: "refund-" + suffix, playerID: playerID, walletID: walletID, kind: wagertx.Refund,
			amount: "25.00", reference: "bet-" + suffix}, wagertx.Processed, "1050.00"},
	}

	for _, step := range steps {
		t.Run(string(step.op.kind), func(t *testing.T) {
			result := i.mustSubmit(t, step.op)
			if result.Transaction.Status() != step.wantStatus {
				t.Fatalf("status = %s, want %s (failureCode=%s)",
					result.Transaction.Status(), step.wantStatus, result.Transaction.FailureCode())
			}
			if got := i.balance(t, walletID); got != step.wantBalance {
				t.Errorf("balance = %s, want %s", got, step.wantBalance)
			}
		})
	}

	if got := i.ledgerCount(t, walletID); got != 4 {
		t.Errorf("ledger entries = %d, want 4 (LOSS must not create one)", got)
	}
}

func TestReversal_SecondOneIsRefused(t *testing.T) {
	i := newInstance(t, "test")
	walletID, playerID := i.openWallet(t, "500.00")
	suffix := uuid.NewString()[:8]
	betID := "bet-" + suffix

	i.mustSubmit(t, operation{externalID: betID, playerID: playerID, walletID: walletID,
		kind: wagertx.Bet, amount: "100.00"})

	refund := i.mustSubmit(t, operation{externalID: "refund-" + suffix, playerID: playerID, walletID: walletID,
		kind: wagertx.Refund, amount: "100.00", reference: betID})
	if refund.Transaction.Status() != wagertx.Processed {
		t.Fatalf("refund status = %s, want PROCESSED", refund.Transaction.Status())
	}

	rollback := i.mustSubmit(t, operation{externalID: "rollback-" + suffix, playerID: playerID, walletID: walletID,
		kind: wagertx.Rollback, amount: "100.00", reference: betID})
	if rollback.Transaction.Status() != wagertx.Rejected {
		t.Fatalf("rollback status = %s, want REJECTED", rollback.Transaction.Status())
	}
	if code := rollback.Transaction.FailureCode(); code != wagertx.FailureDuplicateReversal {
		t.Errorf("failure code = %s, want %s", code, wagertx.FailureDuplicateReversal)
	}

	if got := i.balance(t, walletID); got != "500.00" {
		t.Errorf("balance = %s, want 500.00", got)
	}
}

func TestRollbackOfWin_UsesItsOwnFailureCode(t *testing.T) {
	i := newInstance(t, "test")
	walletID, playerID := i.openWallet(t, "0.00")
	suffix := uuid.NewString()[:8]
	winID := "win-" + suffix

	i.mustSubmit(t, operation{externalID: winID, playerID: playerID, walletID: walletID,
		kind: wagertx.Win, amount: "100.00"})

	i.mustSubmit(t, operation{externalID: "spend-" + suffix, playerID: playerID, walletID: walletID,
		kind: wagertx.Bet, amount: "100.00"})

	rollback := i.mustSubmit(t, operation{externalID: "rb-" + suffix, playerID: playerID, walletID: walletID,
		kind: wagertx.Rollback, amount: "100.00", reference: winID})

	if rollback.Transaction.Status() != wagertx.Rejected {
		t.Fatalf("status = %s, want REJECTED", rollback.Transaction.Status())
	}
	if code := rollback.Transaction.FailureCode(); code != wagertx.FailureInsufficientBalanceForReversal {
		t.Errorf("failure code = %s, want %s (it must differ from a plain insufficient-balance bet)",
			code, wagertx.FailureInsufficientBalanceForReversal)
	}
}

func TestIdempotency_ReplayReturnsTheOriginalBalance(t *testing.T) {
	i := newInstance(t, "test")
	walletID, playerID := i.openWallet(t, "1000.00")
	suffix := uuid.NewString()[:8]

	bet := operation{externalID: "bet-" + suffix, playerID: playerID, walletID: walletID,
		kind: wagertx.Bet, amount: "25.00"}
	first := i.mustSubmit(t, bet)
	if first.Balance.DecimalString() != "975.00" {
		t.Fatalf("first balance = %s, want 975.00", first.Balance.DecimalString())
	}

	i.mustSubmit(t, operation{externalID: "other-" + suffix, playerID: playerID, walletID: walletID,
		kind: wagertx.Bet, amount: "100.00"})
	if got := i.balance(t, walletID); got != "875.00" {
		t.Fatalf("balance after second bet = %s, want 875.00", got)
	}

	replay := i.mustSubmit(t, bet)
	if !replay.IdempotentReplay {
		t.Error("idempotentReplay = false, want true")
	}
	if replay.Transaction.ID() != first.Transaction.ID() {
		t.Error("replay returned a different transaction id")
	}
	if replay.Balance.DecimalString() != "975.00" {
		t.Errorf("replay balance = %s, want 975.00 (the balance at original processing, not the current 875.00)",
			replay.Balance.DecimalString())
	}
	if got := i.ledgerCount(t, walletID); got != 3 {
		t.Errorf("ledger entries = %d, want 3 (opening + two bets, the replay adds none)", got)
	}
}

func TestIdempotency_SameKeyDifferentPayloadConflicts(t *testing.T) {
	i := newInstance(t, "test")
	walletID, playerID := i.openWallet(t, "1000.00")
	suffix := uuid.NewString()[:8]

	i.mustSubmit(t, operation{externalID: "bet-" + suffix, playerID: playerID, walletID: walletID,
		kind: wagertx.Bet, amount: "25.00"})

	_, err := i.submit(t, operation{externalID: "bet-" + suffix, playerID: playerID, walletID: walletID,
		kind: wagertx.Bet, amount: "99.00"})
	if !errors.Is(err, app.ErrIdempotencyConflict) {
		t.Fatalf("error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestIdempotency_SameOperationUnderAnotherKeyConflicts(t *testing.T) {
	i := newInstance(t, "test")
	walletID, playerID := i.openWallet(t, "1000.00")
	suffix := uuid.NewString()[:8]
	externalID := "bet-" + suffix

	i.mustSubmit(t, operation{externalID: externalID, playerID: playerID, walletID: walletID,
		kind: wagertx.Bet, amount: "25.00"})

	_, err := i.submit(t, operation{externalID: externalID, key: "a-completely-different-key-" + suffix,
		playerID: playerID, walletID: walletID, kind: wagertx.Bet, amount: "25.00"})
	if !errors.Is(err, app.ErrOperationIdentityConflict) {
		t.Fatalf("error = %v, want ErrOperationIdentityConflict", err)
	}

	if got := i.ledgerCount(t, walletID); got != 2 {
		t.Errorf("ledger entries = %d, want 2 (opening + one bet)", got)
	}
}

func TestCrossChannel_HTTPThenSQS(t *testing.T) {
	i := newInstance(t, "test")
	walletID, playerID := i.openWallet(t, "1000.00")
	suffix := uuid.NewString()[:8]

	viaHTTP := operation{externalID: "x-" + suffix, playerID: playerID, walletID: walletID,
		kind: wagertx.Bet, amount: "40.00"}
	first := i.mustSubmit(t, viaHTTP)

	viaSQS := viaHTTP
	viaSQS.messageID = "msg-" + suffix
	second := i.mustSubmit(t, viaSQS)

	if !second.IdempotentReplay {
		t.Error("the SQS delivery was not recognized as a replay")
	}
	if second.Transaction.ID() != first.Transaction.ID() {
		t.Error("the SQS delivery created a different transaction")
	}
	if got := i.balance(t, walletID); got != "960.00" {
		t.Errorf("balance = %s, want 960.00 (debited exactly once)", got)
	}

	third := i.mustSubmit(t, viaSQS)
	if !third.IdempotentReplay || third.Transaction.ID() != first.Transaction.ID() {
		t.Error("the message redelivery was not deduplicated by the inbox")
	}
	if got := i.ledgerCount(t, walletID); got != 2 {
		t.Errorf("ledger entries = %d, want 2", got)
	}
}

func TestPendingReference_ResolvesWhenTheTargetArrives(t *testing.T) {
	i := newInstance(t, "test")
	walletID, playerID := i.openWallet(t, "100.00")
	suffix := uuid.NewString()[:8]
	betID := "late-bet-" + suffix

	parked := i.mustSubmit(t, operation{externalID: "early-refund-" + suffix, playerID: playerID, walletID: walletID,
		kind: wagertx.Refund, amount: "40.00", reference: betID})
	if parked.Transaction.Status() != wagertx.PendingReference {
		t.Fatalf("status = %s, want PENDING_REFERENCE", parked.Transaction.Status())
	}

	i.mustSubmit(t, operation{externalID: betID, playerID: playerID, walletID: walletID,
		kind: wagertx.Bet, amount: "40.00"})
	if got := i.balance(t, walletID); got != "60.00" {
		t.Fatalf("balance after the bet = %s, want 60.00", got)
	}

	if _, err := i.resolve.RunOnce(context.Background()); err != nil {
		t.Fatalf("resolving references: %v", err)
	}

	settled, err := i.queries.GetTransaction(context.Background(), internalIdentity, parked.Transaction.ID())
	if err != nil {
		t.Fatalf("reading settled transaction: %v", err)
	}
	if settled.Status() != wagertx.Processed {
		t.Fatalf("status = %s, want PROCESSED (failureCode=%s)", settled.Status(), settled.FailureCode())
	}
	if got := i.balance(t, walletID); got != "100.00" {
		t.Errorf("balance = %s, want 100.00 (the refund gave the stake back)", got)
	}
}

func TestReconciliation_MatchesTheLedger(t *testing.T) {
	i := newInstance(t, "test")
	walletID, playerID := i.openWallet(t, "1000.00")
	suffix := uuid.NewString()[:8]

	i.mustSubmit(t, operation{externalID: "b1-" + suffix, playerID: playerID, walletID: walletID, kind: wagertx.Bet, amount: "10.00"})
	i.mustSubmit(t, operation{externalID: "b2-" + suffix, playerID: playerID, walletID: walletID, kind: wagertx.Bet, amount: "0.55"})
	i.mustSubmit(t, operation{externalID: "w1-" + suffix, playerID: playerID, walletID: walletID, kind: wagertx.Win, amount: "3.33"})
	i.mustSubmit(t, operation{externalID: "l1-" + suffix, playerID: playerID, walletID: walletID, kind: wagertx.Loss, amount: "0.00"})

	result, err := i.queries.Reconcile(context.Background(), internalIdentity, walletID)
	if err != nil {
		t.Fatalf("reconciling: %v", err)
	}

	if !result.Consistent {
		t.Errorf("reconciliation inconsistent: stored=%s calculated=%s difference=%s",
			result.StoredBalance.DecimalString(), result.CalculatedBalance.DecimalString(),
			result.Difference.DecimalString())
	}
	if got := result.StoredBalance.DecimalString(); got != "992.78" {
		t.Errorf("stored balance = %s, want 992.78", got)
	}
	if result.CheckedEntries != 4 {
		t.Errorf("checked entries = %d, want 4 (LOSS contributes none)", result.CheckedEntries)
	}
}

func TestProviderIsolation(t *testing.T) {
	i := newInstance(t, "test")
	walletID, playerID := i.openWallet(t, "100.00")
	suffix := uuid.NewString()[:8]

	owned := i.mustSubmit(t, operation{providerID: "provider-a", externalID: "iso-" + suffix,
		playerID: playerID, walletID: walletID, kind: wagertx.Bet, amount: "10.00"})

	if _, err := i.queries.GetTransaction(context.Background(), providerIdentity("provider-a"), owned.Transaction.ID()); err != nil {
		t.Fatalf("the owning provider could not read its own transaction: %v", err)
	}

	_, err := i.queries.GetTransaction(context.Background(), providerIdentity("provider-b"), owned.Transaction.ID())
	if !errors.Is(err, app.ErrNotFound) {
		t.Errorf("cross-provider read error = %v, want ErrNotFound", err)
	}

	_, err = i.queries.GetProviderTransaction(context.Background(), providerIdentity("provider-b"), "provider-a", "iso-"+suffix)
	if !errors.Is(err, app.ErrNotFound) {
		t.Errorf("cross-provider lookup error = %v, want ErrNotFound", err)
	}

	if _, err := i.queries.GetWallet(context.Background(), providerIdentity("provider-a"), walletID); !errors.Is(err, app.ErrForbidden) {
		t.Errorf("provider wallet read error = %v, want ErrForbidden", err)
	}
	if _, err := i.queries.Reconcile(context.Background(), providerIdentity("provider-a"), walletID); !errors.Is(err, app.ErrForbidden) {
		t.Errorf("provider reconciliation error = %v, want ErrForbidden", err)
	}
}
