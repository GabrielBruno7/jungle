//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"jungle/internal/domain/wagertx"
)

func TestRecovery_RestartPreservesIdempotencyAndConsistency(t *testing.T) {
	before := newInstance(t, "before-restart")
	walletID, playerID := before.openWallet(t, "1000.00")
	suffix := uuid.NewString()[:8]

	httpOp := operation{externalID: "http-bet-" + suffix, playerID: playerID, walletID: walletID,
		kind: wagertx.Bet, amount: "25.00"}
	queueOp := operation{externalID: "sqs-bet-" + suffix, playerID: playerID, walletID: walletID,
		kind: wagertx.Bet, amount: "15.00", messageID: "msg-" + suffix}

	first := before.mustSubmit(t, httpOp)
	before.mustSubmit(t, queueOp)

	if got := before.balance(t, walletID); got != "960.00" {
		t.Fatalf("balance before the restart = %s, want 960.00", got)
	}

	before.shutdown()

	after := newInstance(t, "after-restart")

	if got := after.balance(t, walletID); got != "960.00" {
		t.Fatalf("balance after the restart = %s, want 960.00", got)
	}

	replay := after.mustSubmit(t, httpOp)
	if !replay.IdempotentReplay {
		t.Error("the HTTP operation was reprocessed after the restart instead of replayed")
	}
	if replay.Transaction.ID() != first.Transaction.ID() {
		t.Errorf("replay produced transaction %s, want the original %s", replay.Transaction.ID(), first.Transaction.ID())
	}
	if got := replay.Balance.DecimalString(); got != "975.00" {
		t.Errorf("replayed balance = %s, want 975.00 (the balance observed at the time)", got)
	}

	redelivered := after.mustSubmit(t, queueOp)
	if !redelivered.IdempotentReplay {
		t.Error("the redelivered message was reprocessed after the restart; the inbox did not survive it")
	}

	if got := after.balance(t, walletID); got != "960.00" {
		t.Errorf("balance = %s, want 960.00 (no replay may move money)", got)
	}
	if got := after.ledgerCount(t, walletID); got != 3 {
		t.Errorf("ledger entries = %d, want 3 (opening, one bet, one bet)", got)
	}

	result, err := after.queries.Reconcile(context.Background(), internalIdentity, walletID)
	if err != nil {
		t.Fatalf("reconciling after the restart: %v", err)
	}
	if !result.Consistent {
		t.Errorf("reconciliation inconsistent after the restart: stored=%s calculated=%s",
			result.StoredBalance.DecimalString(), result.CalculatedBalance.DecimalString())
	}
}

func TestRecovery_AnotherInstanceSettlesAParkedReversal(t *testing.T) {
	accepting := newInstance(t, "accepting-instance")
	walletID, playerID := accepting.openWallet(t, "100.00")
	suffix := uuid.NewString()[:8]
	betID := "late-bet-" + suffix

	parked := accepting.mustSubmit(t, operation{externalID: "early-refund-" + suffix, playerID: playerID,
		walletID: walletID, kind: wagertx.Refund, amount: "40.00", reference: betID})
	if parked.Transaction.Status() != wagertx.PendingReference {
		t.Fatalf("status = %s, want PENDING_REFERENCE", parked.Transaction.Status())
	}

	accepting.shutdown()

	settling := newInstance(t, "settling-instance")
	settling.mustSubmit(t, operation{externalID: betID, playerID: playerID, walletID: walletID,
		kind: wagertx.Bet, amount: "40.00"})

	moved, err := settling.resolve.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("resolving references on the second instance: %v", err)
	}
	if moved == 0 {
		t.Fatal("the second instance settled nothing; a parked reversal outlived the instance that accepted it")
	}

	settled, err := settling.queries.GetTransaction(context.Background(), internalIdentity, parked.Transaction.ID())
	if err != nil {
		t.Fatalf("reading the parked transaction: %v", err)
	}
	if settled.Status() != wagertx.Processed {
		t.Fatalf("status = %s, want PROCESSED (failureCode=%s)", settled.Status(), settled.FailureCode())
	}
	if got := settling.balance(t, walletID); got != "100.00" {
		t.Errorf("balance = %s, want 100.00 (bet of 40 then its refund)", got)
	}
}
