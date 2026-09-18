//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"jungle/internal/app"
	"jungle/internal/domain/wagertx"
)

func TestConcurrency_TwoBetsOneWallet(t *testing.T) {
	instances := []*instance{
		newInstance(t, "instance-1"),
		newInstance(t, "instance-2"),
		newInstance(t, "instance-3"),
	}

	walletID, playerID := instances[0].openWallet(t, "100.00")
	suffix := uuid.NewString()[:8]

	type outcome struct {
		result app.ProcessWagerResult
		err    error
	}
	outcomes := make([]outcome, 2)

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup

	for idx := range 2 {
		done.Add(1)
		go func(idx int) {
			defer done.Done()

			inst := instances[idx]
			op := operation{
				externalID: fmt.Sprintf("race-%s-%d", suffix, idx),
				playerID:   playerID,
				walletID:   walletID,
				kind:       wagertx.Bet,
				amount:     "80.00",
			}
			start.Wait()
			result, err := inst.submit(t, op)
			outcomes[idx] = outcome{result: result, err: err}
		}(idx)
	}

	start.Done()
	done.Wait()

	processed, rejected := 0, 0
	for _, o := range outcomes {
		if o.err != nil {
			t.Fatalf("unexpected error: %v", o.err)
		}
		switch o.result.Transaction.Status() {
		case wagertx.Processed:
			processed++
		case wagertx.Rejected:
			rejected++
			if code := o.result.Transaction.FailureCode(); code != wagertx.FailureInsufficientBalance {
				t.Errorf("rejection code = %s, want %s", code, wagertx.FailureInsufficientBalance)
			}
		default:
			t.Errorf("unexpected status %s", o.result.Transaction.Status())
		}
	}

	if processed != 1 || rejected != 1 {
		t.Fatalf("got %d processed and %d rejected, want exactly 1 and 1", processed, rejected)
	}

	verifier := instances[2]
	if got := verifier.balance(t, walletID); got != "20.00" {
		t.Errorf("final balance = %s, want 20.00", got)
	}
	if got := verifier.ledgerCount(t, walletID); got != 2 {
		t.Errorf("ledger entries = %d, want 2 (the opening credit and exactly one debit)", got)
	}

	for idx := range 2 {
		op := operation{
			externalID: fmt.Sprintf("race-%s-%d", suffix, idx),
			playerID:   playerID,
			walletID:   walletID,
			kind:       wagertx.Bet,
			amount:     "80.00",
		}
		result := instances[idx].mustSubmit(t, op)
		if !result.IdempotentReplay {
			t.Errorf("resubmission %d was not recognized as a replay", idx)
		}
	}
	if got := verifier.balance(t, walletID); got != "20.00" {
		t.Errorf("balance after resubmission = %s, want 20.00", got)
	}
}

func TestConcurrency_SameBetFiftyTimes(t *testing.T) {
	instances := []*instance{
		newInstance(t, "instance-1"),
		newInstance(t, "instance-2"),
		newInstance(t, "instance-3"),
	}

	walletID, playerID := instances[0].openWallet(t, "500.00")
	suffix := uuid.NewString()[:8]

	op := operation{
		externalID: "dup-" + suffix,
		playerID:   playerID,
		walletID:   walletID,
		kind:       wagertx.Bet,
		amount:     "10.00",
	}

	const attempts = 50
	ids := make([]uuid.UUID, attempts)
	errs := make([]error, attempts)

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup

	for i := range attempts {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			inst := instances[i%len(instances)]
			start.Wait()
			result, err := inst.submit(t, op)
			ids[i], errs[i] = result.Transaction.ID(), err
		}(i)
	}

	start.Done()
	done.Wait()

	distinct := map[uuid.UUID]bool{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("attempt %d failed: %v", i, err)
		}
		distinct[ids[i]] = true
	}

	if len(distinct) != 1 {
		t.Errorf("produced %d distinct transactions, want exactly 1", len(distinct))
	}
	if got := instances[0].balance(t, walletID); got != "490.00" {
		t.Errorf("balance = %s, want 490.00 (debited exactly once)", got)
	}
	if got := instances[0].ledgerCount(t, walletID); got != 2 {
		t.Errorf("ledger entries = %d, want 2 (opening + one debit)", got)
	}
}

func TestConcurrency_DifferentWalletsProgressInParallel(t *testing.T) {
	i := newInstance(t, "test")
	suffix := uuid.NewString()[:8]

	const wallets = 10
	walletIDs := make([]uuid.UUID, wallets)
	playerIDs := make([]uuid.UUID, wallets)
	for w := range wallets {
		walletIDs[w], playerIDs[w] = i.openWallet(t, "100.00")
	}

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	errs := make([]error, wallets)

	for w := range wallets {
		done.Add(1)
		go func(w int) {
			defer done.Done()
			start.Wait()
			_, errs[w] = i.submit(t, operation{
				externalID: fmt.Sprintf("par-%s-%d", suffix, w),
				playerID:   playerIDs[w],
				walletID:   walletIDs[w],
				kind:       wagertx.Bet,
				amount:     "30.00",
			})
		}(w)
	}

	start.Done()
	done.Wait()

	for w, err := range errs {
		if err != nil {
			t.Fatalf("wallet %d failed: %v", w, err)
		}
		if got := i.balance(t, walletIDs[w]); got != "70.00" {
			t.Errorf("wallet %d balance = %s, want 70.00", w, got)
		}
	}
}

func TestLedgerIsAppendOnly(t *testing.T) {
	i := newInstance(t, "test")
	walletID, _ := i.openWallet(t, "100.00")

	var entryID uuid.UUID
	if err := i.pool.QueryRow(context.Background(),
		`SELECT id FROM wallet_ledger_entry WHERE wallet_id = $1 LIMIT 1`, walletID).Scan(&entryID); err != nil {
		t.Fatalf("reading a ledger entry: %v", err)
	}

	if _, err := i.pool.Exec(context.Background(),
		`UPDATE wallet_ledger_entry SET amount_minor_units = 1 WHERE id = $1`, entryID); err == nil {
		t.Error("the database allowed a ledger entry to be updated")
	}
	if _, err := i.pool.Exec(context.Background(),
		`DELETE FROM wallet_ledger_entry WHERE id = $1`, entryID); err == nil {
		t.Error("the database allowed a ledger entry to be deleted")
	}
}

func TestTerminalTransactionsAreFrozen(t *testing.T) {
	i := newInstance(t, "test")
	walletID, playerID := i.openWallet(t, "100.00")
	suffix := uuid.NewString()[:8]

	result := i.mustSubmit(t, operation{externalID: "frozen-" + suffix, playerID: playerID,
		walletID: walletID, kind: wagertx.Bet, amount: "10.00"})

	if _, err := i.pool.Exec(context.Background(),
		`UPDATE wager_transaction SET status = 'REJECTED' WHERE id = $1`, result.Transaction.ID()); err == nil {
		t.Error("the database allowed a terminal transaction to be modified")
	}
}

func TestWalletBalanceCannotGoNegative(t *testing.T) {
	i := newInstance(t, "test")
	walletID, _ := i.openWallet(t, "100.00")

	if _, err := i.pool.Exec(context.Background(),
		`UPDATE wallet SET balance_minor_units = -1 WHERE id = $1`, walletID); err == nil {
		t.Error("the database allowed a negative balance")
	}
}

func TestOutbox_TwoPublishersNeverDoubleSend(t *testing.T) {
	first := newInstance(t, "publisher-1")
	second := newInstance(t, "publisher-2")

	walletID, playerID := first.openWallet(t, "1000.00")
	suffix := uuid.NewString()[:8]
	for n := range 5 {
		first.mustSubmit(t, operation{
			externalID: fmt.Sprintf("ob-%s-%d", suffix, n),
			playerID:   playerID, walletID: walletID,
			kind: wagertx.Bet, amount: "1.00",
		})
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = first.outbox.RunOnce(context.Background()) }()
	go func() { defer wg.Done(); _, _ = second.outbox.RunOnce(context.Background()) }()
	wg.Wait()

	seen := map[string]int{}
	for _, id := range append(first.publisher.eventIDs(), second.publisher.eventIDs()...) {
		seen[id]++
	}
	for id, count := range seen {
		if count > 1 {
			t.Errorf("event %s was published %d times by competing publishers", id, count)
		}
	}
	if len(seen) == 0 {
		t.Error("no events were published at all")
	}

	var unpublished int
	if err := first.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&unpublished); err != nil {
		t.Fatalf("counting unpublished events: %v", err)
	}
	t.Logf("events still pending after one pass each: %d", unpublished)
}

func TestOutbox_RepublishKeepsTheSameEventID(t *testing.T) {
	i := newInstance(t, "flaky-publisher")
	walletID, playerID := i.openWallet(t, "100.00")
	suffix := uuid.NewString()[:8]

	result := i.mustSubmit(t, operation{externalID: "rp-" + suffix, playerID: playerID,
		walletID: walletID, kind: wagertx.Bet, amount: "5.00"})

	ctx := context.Background()
	var eventID uuid.UUID
	if err := i.pool.QueryRow(ctx,
		`SELECT event_id FROM outbox WHERE aggregate_id = $1 AND published_at IS NULL LIMIT 1`,
		result.Transaction.ID().String()).Scan(&eventID); err != nil {
		t.Fatalf("finding the event this operation produced: %v", err)
	}

	firstClaim := claimSpecificEvent(t, i, eventID, "publisher-a")
	if firstClaim.EventID != eventID {
		t.Fatalf("claimed %s, want %s", firstClaim.EventID, eventID)
	}

	secondClaim := claimSpecificEvent(t, i, eventID, "publisher-b")

	if secondClaim.EventID != firstClaim.EventID {
		t.Errorf("reclaimed event has id %s but the abandoned one was %s; a republish must preserve the eventId",
			secondClaim.EventID, firstClaim.EventID)
	}
	if string(secondClaim.Payload) != string(firstClaim.Payload) {
		t.Error("the reclaimed event carries a different payload; the snapshot must be immutable")
	}
	if secondClaim.AggregateID != firstClaim.AggregateID {
		t.Error("the reclaimed event changed aggregate")
	}
}

func claimSpecificEvent(t *testing.T, i *instance, eventID uuid.UUID, owner string) app.OutboxRecord {
	t.Helper()

	var claimed app.OutboxRecord
	err := i.uow.Within(context.Background(), func(ctx context.Context, repos app.Repositories) error {
		if _, err := i.pool.Exec(ctx,
			`UPDATE outbox SET locked_by = NULL, locked_until = NULL, published_at = NULL,
			        next_attempt_at = to_timestamp(0)
			 WHERE event_id = $1`, eventID); err != nil {
			return err
		}

		var found bool
		for attempt := 0; attempt < 5 && !found; attempt++ {
			batch, err := repos.Outbox().Claim(ctx, owner, 100, 30*time.Second, time.Now())
			if err != nil {
				return err
			}
			for _, rec := range batch {
				if rec.EventID == eventID {
					claimed, found = rec, true
					break
				}
			}
			if len(batch) == 0 {
				break
			}
		}
		if !found {
			return fmt.Errorf("event %s was not returned by any claim", eventID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("claiming as %s: %v", owner, err)
	}
	return claimed
}

func TestConcurrencyConflictIsReported(t *testing.T) {
	i := newInstance(t, "test")
	walletID, _ := i.openWallet(t, "100.00")

	w, err := i.queries.GetWallet(context.Background(), internalIdentity, walletID)
	if err != nil {
		t.Fatalf("reading wallet: %v", err)
	}

	if _, err := i.pool.Exec(context.Background(),
		`UPDATE wallet SET version = version + 1 WHERE id = $1`, walletID); err != nil {
		t.Fatalf("bumping version: %v", err)
	}

	err = i.uow.Within(context.Background(), func(ctx context.Context, repos app.Repositories) error {
		return repos.Wallets().UpdateBalance(ctx, w, w.Version())
	})
	if !errors.Is(err, app.ErrConcurrencyConflict) {
		t.Fatalf("error = %v, want ErrConcurrencyConflict", err)
	}
}
