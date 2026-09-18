//go:build integration

package integration

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"jungle/internal/domain/wagertx"
)

func TestSQSConsumer_AppliesMessageAndDeletesIt(t *testing.T) {
	inst := newInstance(t, "sqs-e2e")
	queues := newTestQueues(t, 5)
	walletID, playerID := inst.openWallet(t, "1000.00")

	suffix := uuid.NewString()[:8]
	op := operation{
		externalID: "sqs-" + suffix,
		playerID:   playerID,
		walletID:   walletID,
		kind:       wagertx.Bet,
		amount:     "25.00",
	}
	messageID := "msg-" + suffix

	queues.send(t, walletID.String(), "delivery-1", envelopeFor(t, op, messageID))
	startConsumer(t, inst, queues, 30)

	waitFor(t, 30*time.Second, "the bet to be debited", func() bool {
		return inst.balance(t, walletID) == "975.00"
	})
	waitFor(t, 20*time.Second, "the handled message to be deleted from the queue", func() bool {
		return queues.depth(t, queues.main) == 0
	})

	if got := inst.ledgerCount(t, walletID); got != 2 {
		t.Errorf("ledger entries = %d, want 2 (the opening credit and one debit)", got)
	}
	if got := inst.inboxCount(t, messageID); got != 1 {
		t.Errorf("inbox rows = %d, want 1", got)
	}
	if got := inst.transactionCount(t, op.externalID); got != 1 {
		t.Errorf("transactions = %d, want 1", got)
	}
}

func TestSQSConsumer_RedeliveryOfTheSameMessageIsDeduplicated(t *testing.T) {
	inst := newInstance(t, "sqs-redelivery")
	queues := newTestQueues(t, 5)
	walletID, playerID := inst.openWallet(t, "1000.00")

	suffix := uuid.NewString()[:8]
	op := operation{
		externalID: "sqs-dup-" + suffix,
		playerID:   playerID,
		walletID:   walletID,
		kind:       wagertx.Bet,
		amount:     "40.00",
	}
	messageID := "msg-dup-" + suffix
	body := envelopeFor(t, op, messageID)

	queues.send(t, walletID.String(), "delivery-1", body)
	queues.send(t, walletID.String(), "delivery-2", body)

	startConsumer(t, inst, queues, 30)

	waitFor(t, 30*time.Second, "both deliveries to be consumed", func() bool {
		return queues.depth(t, queues.main) == 0
	})

	if got := inst.balance(t, walletID); got != "960.00" {
		t.Errorf("balance = %s, want 960.00 (debited exactly once despite two deliveries)", got)
	}
	if got := inst.ledgerCount(t, walletID); got != 2 {
		t.Errorf("ledger entries = %d, want 2", got)
	}
	if got := inst.inboxCount(t, messageID); got != 1 {
		t.Errorf("inbox rows = %d, want 1", got)
	}
	if got := inst.transactionCount(t, op.externalID); got != 1 {
		t.Errorf("transactions = %d, want 1", got)
	}
}

func TestSQSConsumer_MalformedMessageIsNotDeletedAndReachesTheDLQ(t *testing.T) {
	inst := newInstance(t, "sqs-malformed")
	queues := newTestQueues(t, 1)

	queues.send(t, "malformed-group", "malformed-1", "{ this is not valid json")
	startConsumer(t, inst, queues, 0)

	waitFor(t, 45*time.Second, "the malformed message to be moved to the dead letter queue", func() bool {
		return queues.depth(t, queues.dlq) == 1
	})

	if got := queues.depth(t, queues.main); got != 0 {
		t.Errorf("messages left on the main queue = %d, want 0 (it should have been redriven, never deleted)", got)
	}
}

func TestSQSConsumer_RefusesOpeningKindFromTheQueue(t *testing.T) {
	inst := newInstance(t, "sqs-opening")
	queues := newTestQueues(t, 1)
	walletID, playerID := inst.openWallet(t, "500.00")

	suffix := uuid.NewString()[:8]
	op := operation{
		externalID: "sqs-opening-" + suffix,
		playerID:   playerID,
		walletID:   walletID,
		kind:       wagertx.Opening,
		amount:     "1000000.00",
	}
	messageID := "msg-opening-" + suffix

	queues.send(t, walletID.String(), "opening-1", envelopeFor(t, op, messageID))
	startConsumer(t, inst, queues, 0)

	waitFor(t, 45*time.Second, "the refused OPENING to be moved to the dead letter queue", func() bool {
		return queues.depth(t, queues.dlq) == 1
	})

	if got := inst.balance(t, walletID); got != "500.00" {
		t.Errorf("balance = %s, want 500.00 (a refused OPENING must credit nothing)", got)
	}
	if got := inst.ledgerCount(t, walletID); got != 1 {
		t.Errorf("ledger entries = %d, want 1 (only the opening credit from wallet creation)", got)
	}
	if got := inst.transactionCount(t, op.externalID); got != 0 {
		t.Errorf("transactions = %d, want 0", got)
	}
	if got := inst.inboxCount(t, messageID); got != 0 {
		t.Errorf("inbox rows = %d, want 0", got)
	}
}

func TestSQSConsumer_RacesTheHTTPPathOnTheSameOperation(t *testing.T) {
	inst := newInstance(t, "sqs-vs-http")
	queues := newTestQueues(t, 5)
	walletID, playerID := inst.openWallet(t, "1000.00")

	suffix := uuid.NewString()[:8]
	op := operation{
		externalID: "both-doors-" + suffix,
		playerID:   playerID,
		walletID:   walletID,
		kind:       wagertx.Bet,
		amount:     "30.00",
	}
	messageID := "msg-both-" + suffix

	queues.send(t, walletID.String(), "delivery-1", envelopeFor(t, op, messageID))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		startConsumer(t, inst, queues, 30)
	}()

	direct := op
	direct.messageID = ""
	result, err := inst.submit(t, direct)
	if err != nil {
		t.Fatalf("submitting the same operation over the direct path: %v", err)
	}
	wg.Wait()

	waitFor(t, 30*time.Second, "the queued copy to be consumed", func() bool {
		return queues.depth(t, queues.main) == 0
	})

	if got := inst.balance(t, walletID); got != "970.00" {
		t.Errorf("balance = %s, want 970.00 (debited exactly once across both entry points)", got)
	}
	if got := inst.ledgerCount(t, walletID); got != 2 {
		t.Errorf("ledger entries = %d, want 2", got)
	}
	if got := inst.transactionCount(t, op.externalID); got != 1 {
		t.Errorf("transactions = %d, want 1 (both doors must resolve to one operation)", got)
	}

	stored, err := inst.queries.GetTransaction(t.Context(), internalIdentity, result.Transaction.ID())
	if err != nil {
		t.Fatalf("reading the settled transaction: %v", err)
	}
	if stored.Status() != wagertx.Processed {
		t.Errorf("status = %s, want PROCESSED", stored.Status())
	}
}

func TestSQSConsumer_InterruptedAfterCommitBeforeDelete(t *testing.T) {
	inst := newInstance(t, "sqs-crash")
	queues := newTestQueues(t, 5)
	walletID, playerID := inst.openWallet(t, "1000.00")

	suffix := uuid.NewString()[:8]
	op := operation{
		externalID: "crash-" + suffix,
		playerID:   playerID,
		walletID:   walletID,
		kind:       wagertx.Bet,
		amount:     "60.00",
	}
	messageID := "msg-crash-" + suffix

	queues.send(t, walletID.String(), "delivery-1", envelopeFor(t, op, messageID))

	message, received := queues.receiveOne(t, 30)
	if !received {
		t.Fatal("the message was not delivered for the simulated crash")
	}

	committed := op
	committed.messageID = messageID
	inst.mustSubmit(t, committed)

	if got := inst.balance(t, walletID); got != "940.00" {
		t.Fatalf("balance after the committed handling = %s, want 940.00", got)
	}

	queues.makeVisible(t, message.ReceiptHandle)
	startConsumer(t, inst, queues, 30)

	waitFor(t, 30*time.Second, "the redelivered message to be consumed and deleted", func() bool {
		return queues.depth(t, queues.main) == 0
	})

	if got := inst.balance(t, walletID); got != "940.00" {
		t.Errorf("balance = %s, want 940.00 (the redelivery must not debit again)", got)
	}
	if got := inst.ledgerCount(t, walletID); got != 2 {
		t.Errorf("ledger entries = %d, want 2", got)
	}
	if got := inst.inboxCount(t, messageID); got != 1 {
		t.Errorf("inbox rows = %d, want 1", got)
	}
	if got := inst.transactionCount(t, op.externalID); got != 1 {
		t.Errorf("transactions = %d, want 1", got)
	}
}
