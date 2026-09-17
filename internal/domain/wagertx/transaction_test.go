package wagertx_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"jungle/internal/domain/money"
	"jungle/internal/domain/wagertx"
)

func mustMoney(t *testing.T, s string) money.Money {
	t.Helper()
	m, err := money.Parse(s, money.BRL)
	if err != nil {
		t.Fatalf("money.Parse(%q) unexpected error: %v", s, err)
	}
	return m
}

func validExternalParams(kind wagertx.Kind) wagertx.NewExternalParams {
	p := wagertx.NewExternalParams{
		ID:                    uuid.New(),
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		IdempotencyKey:        "provider-a:transaction-123",
		PayloadHash:           "deadbeef",
		WalletID:              uuid.New(),
		PlayerID:              uuid.New(),
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  kind,
		Now:                   time.Now(),
	}

	switch kind {
	case wagertx.Loss:
		p.Money = money.Zero(money.BRL)
	case wagertx.Refund, wagertx.Rollback:
		p.Money = money.FromMinorUnits(2500, money.BRL)
		p.ReferenceExternalTransactionID = "transaction-100"
	default:
		p.Money = money.FromMinorUnits(2500, money.BRL)
	}

	return p
}

func TestNewExternal_ValidPerKind(t *testing.T) {
	for _, kind := range []wagertx.Kind{wagertx.Bet, wagertx.Win, wagertx.Loss, wagertx.Refund, wagertx.Rollback} {
		t.Run(string(kind), func(t *testing.T) {
			tx, err := wagertx.NewExternal(validExternalParams(kind))
			if err != nil {
				t.Fatalf("NewExternal(%s) unexpected error: %v", kind, err)
			}
			if tx.Status() != wagertx.Pending {
				t.Errorf("Status() = %s, want PENDING", tx.Status())
			}
			if tx.IsTerminal() {
				t.Errorf("IsTerminal() = true for a freshly created PENDING transaction")
			}
		})
	}
}

func TestNewExternal_RejectsOpening(t *testing.T) {
	p := validExternalParams(wagertx.Bet)
	p.Kind = wagertx.Opening
	_, err := wagertx.NewExternal(p)
	if !errors.Is(err, wagertx.ErrOpeningNotExternal) {
		t.Fatalf("error = %v, want ErrOpeningNotExternal", err)
	}
}

func TestNewExternal_RejectsMissingMetadata(t *testing.T) {
	p := validExternalParams(wagertx.Bet)
	p.ProviderID = ""
	if _, err := wagertx.NewExternal(p); !errors.Is(err, wagertx.ErrInvalidTransaction) {
		t.Errorf("missing ProviderID: error = %v, want ErrInvalidTransaction", err)
	}

	p = validExternalParams(wagertx.Bet)
	p.IdempotencyKey = ""
	if _, err := wagertx.NewExternal(p); !errors.Is(err, wagertx.ErrInvalidTransaction) {
		t.Errorf("missing IdempotencyKey: error = %v, want ErrInvalidTransaction", err)
	}
}

func TestNewExternal_BetRequiresPositiveAmount(t *testing.T) {
	p := validExternalParams(wagertx.Bet)
	p.Money = money.Zero(money.BRL)
	if _, err := wagertx.NewExternal(p); !errors.Is(err, wagertx.ErrInvalidAmountForKind) {
		t.Fatalf("error = %v, want ErrInvalidAmountForKind", err)
	}
}

func TestNewExternal_LossRequiresZeroAmount(t *testing.T) {
	p := validExternalParams(wagertx.Loss)
	p.Money = money.FromMinorUnits(100, money.BRL)
	if _, err := wagertx.NewExternal(p); !errors.Is(err, wagertx.ErrInvalidAmountForKind) {
		t.Fatalf("error = %v, want ErrInvalidAmountForKind", err)
	}
}

func TestNewExternal_RefundAndRollbackRequireReference(t *testing.T) {
	for _, kind := range []wagertx.Kind{wagertx.Refund, wagertx.Rollback} {
		p := validExternalParams(kind)
		p.ReferenceExternalTransactionID = ""
		if _, err := wagertx.NewExternal(p); !errors.Is(err, wagertx.ErrReferenceRequired) {
			t.Errorf("%s without reference: error = %v, want ErrReferenceRequired", kind, err)
		}
	}
}

func TestNewExternal_BetAndLossRejectReference(t *testing.T) {
	for _, kind := range []wagertx.Kind{wagertx.Bet, wagertx.Loss} {
		p := validExternalParams(kind)
		p.ReferenceExternalTransactionID = "some-bet"
		if _, err := wagertx.NewExternal(p); !errors.Is(err, wagertx.ErrReferenceNotApplicable) {
			t.Errorf("%s with reference: error = %v, want ErrReferenceNotApplicable", kind, err)
		}
	}
}

func TestNewExternal_WinReferenceIsOptional(t *testing.T) {
	withRef := validExternalParams(wagertx.Win)
	withRef.ReferenceExternalTransactionID = "some-bet"
	if _, err := wagertx.NewExternal(withRef); err != nil {
		t.Errorf("WIN with reference: unexpected error: %v", err)
	}

	withoutRef := validExternalParams(wagertx.Win)
	if _, err := wagertx.NewExternal(withoutRef); err != nil {
		t.Errorf("WIN without reference: unexpected error: %v", err)
	}
}

func TestNewOpening_Valid(t *testing.T) {
	tx, err := wagertx.NewOpening(wagertx.NewOpeningParams{
		ID:       uuid.New(),
		WalletID: uuid.New(),
		PlayerID: uuid.New(),
		Money:    mustMoney(t, "1000.00"),
		Now:      time.Now(),
	})
	if err != nil {
		t.Fatalf("NewOpening unexpected error: %v", err)
	}
	if tx.Kind() != wagertx.Opening {
		t.Errorf("Kind() = %s, want OPENING", tx.Kind())
	}
	if tx.Status() != wagertx.Processed {
		t.Errorf("Status() = %s, want PROCESSED (opening is synchronous)", tx.Status())
	}
	if !tx.IsTerminal() {
		t.Errorf("IsTerminal() = false, want true")
	}
	balance, ok := tx.ResultingBalance()
	if !ok || !balance.Equal(mustMoney(t, "1000.00")) {
		t.Errorf("ResultingBalance() = %s, %v, want 1000.00, true", balance, ok)
	}
}

func TestNewOpening_RejectsZeroAmount(t *testing.T) {
	_, err := wagertx.NewOpening(wagertx.NewOpeningParams{
		ID:       uuid.New(),
		WalletID: uuid.New(),
		PlayerID: uuid.New(),
		Money:    money.Zero(money.BRL),
		Now:      time.Now(),
	})
	if !errors.Is(err, wagertx.ErrInvalidAmountForKind) {
		t.Fatalf("error = %v, want ErrInvalidAmountForKind (a zero-balance opening must not create a transaction at all)", err)
	}
}

func TestTransitions_PendingToProcessed(t *testing.T) {
	tx, err := wagertx.NewExternal(validExternalParams(wagertx.Bet))
	if err != nil {
		t.Fatalf("NewExternal unexpected error: %v", err)
	}

	if err := tx.MarkProcessed(mustMoney(t, "975.00"), time.Now()); err != nil {
		t.Fatalf("MarkProcessed unexpected error: %v", err)
	}
	if tx.Status() != wagertx.Processed {
		t.Errorf("Status() = %s, want PROCESSED", tx.Status())
	}
	balance, ok := tx.ResultingBalance()
	if !ok || !balance.Equal(mustMoney(t, "975.00")) {
		t.Errorf("ResultingBalance() = %s, %v, want 975.00, true", balance, ok)
	}
}

func TestTransitions_PendingToRejected_RequiresFailureCode(t *testing.T) {
	tx, err := wagertx.NewExternal(validExternalParams(wagertx.Bet))
	if err != nil {
		t.Fatalf("NewExternal unexpected error: %v", err)
	}

	if err := tx.MarkRejected("", time.Now()); !errors.Is(err, wagertx.ErrInvalidTransaction) {
		t.Errorf("MarkRejected(\"\") error = %v, want ErrInvalidTransaction", err)
	}

	if err := tx.MarkRejected(wagertx.FailureInsufficientBalance, time.Now()); err != nil {
		t.Fatalf("MarkRejected unexpected error: %v", err)
	}
	if tx.Status() != wagertx.Rejected {
		t.Errorf("Status() = %s, want REJECTED", tx.Status())
	}
	if tx.FailureCode() != wagertx.FailureInsufficientBalance {
		t.Errorf("FailureCode() = %s, want %s", tx.FailureCode(), wagertx.FailureInsufficientBalance)
	}
}

func TestTransitions_TerminalNeverTransitionsAgain(t *testing.T) {
	terminalTransitions := []func(*wagertx.WagerTransaction) error{
		func(tx *wagertx.WagerTransaction) error { return tx.MarkProcessed(money.Zero(money.BRL), time.Now()) },
		func(tx *wagertx.WagerTransaction) error {
			return tx.MarkRejected(wagertx.FailureInsufficientBalance, time.Now())
		},
		func(tx *wagertx.WagerTransaction) error {
			return tx.MarkFailed(wagertx.FailureInvalidAmount, time.Now())
		},
	}

	for _, reachTerminal := range terminalTransitions {
		tx, err := wagertx.NewExternal(validExternalParams(wagertx.Bet))
		if err != nil {
			t.Fatalf("NewExternal unexpected error: %v", err)
		}
		if err := reachTerminal(&tx); err != nil {
			t.Fatalf("reaching terminal state: unexpected error: %v", err)
		}

		// Every further transition attempt must fail, whichever one it is.
		if err := tx.MarkProcessed(money.Zero(money.BRL), time.Now()); !errors.Is(err, wagertx.ErrInvalidTransition) {
			t.Errorf("MarkProcessed after terminal: error = %v, want ErrInvalidTransition", err)
		}
		if err := tx.MarkRejected(wagertx.FailureInsufficientBalance, time.Now()); !errors.Is(err, wagertx.ErrInvalidTransition) {
			t.Errorf("MarkRejected after terminal: error = %v, want ErrInvalidTransition", err)
		}
		if err := tx.MarkFailed(wagertx.FailureInvalidAmount, time.Now()); !errors.Is(err, wagertx.ErrInvalidTransition) {
			t.Errorf("MarkFailed after terminal: error = %v, want ErrInvalidTransition", err)
		}
		if err := tx.ResolveReference(uuid.New()); !errors.Is(err, wagertx.ErrTerminalState) {
			t.Errorf("ResolveReference after terminal: error = %v, want ErrTerminalState", err)
		}
	}
}

func TestTransitions_PendingReference_OnlyForRefundAndRollback(t *testing.T) {
	bet, err := wagertx.NewExternal(validExternalParams(wagertx.Bet))
	if err != nil {
		t.Fatalf("NewExternal unexpected error: %v", err)
	}
	if err := bet.MarkPendingReference(time.Now()); !errors.Is(err, wagertx.ErrInvalidTransition) {
		t.Errorf("BET MarkPendingReference: error = %v, want ErrInvalidTransition", err)
	}

	refund, err := wagertx.NewExternal(validExternalParams(wagertx.Refund))
	if err != nil {
		t.Fatalf("NewExternal unexpected error: %v", err)
	}
	if err := refund.MarkPendingReference(time.Now()); err != nil {
		t.Fatalf("REFUND MarkPendingReference: unexpected error: %v", err)
	}
	if refund.Status() != wagertx.PendingReference {
		t.Errorf("Status() = %s, want PENDING_REFERENCE", refund.Status())
	}

	// From PENDING_REFERENCE, the worker can still resolve it to PROCESSED
	// or REJECTED later.
	if err := refund.MarkProcessed(mustMoney(t, "25.00"), time.Now()); err != nil {
		t.Fatalf("MarkProcessed from PENDING_REFERENCE: unexpected error: %v", err)
	}
}

func TestResolveReference(t *testing.T) {
	tx, err := wagertx.NewExternal(validExternalParams(wagertx.Refund))
	if err != nil {
		t.Fatalf("NewExternal unexpected error: %v", err)
	}

	refID := uuid.New()
	if err := tx.ResolveReference(refID); err != nil {
		t.Fatalf("ResolveReference unexpected error: %v", err)
	}
	if tx.ReferenceTransactionID() != refID {
		t.Errorf("ReferenceTransactionID() = %s, want %s", tx.ReferenceTransactionID(), refID)
	}

	if err := tx.ResolveReference(uuid.Nil); !errors.Is(err, wagertx.ErrInvalidTransaction) {
		t.Errorf("ResolveReference(Nil): error = %v, want ErrInvalidTransaction", err)
	}
}

func TestRehydrate_Valid(t *testing.T) {
	now := time.Now()
	tx, err := wagertx.Rehydrate(wagertx.RehydrateParams{
		ID:                    uuid.New(),
		WalletID:              uuid.New(),
		PlayerID:              uuid.New(),
		Kind:                  wagertx.Bet,
		Money:                 mustMoney(t, "25.00"),
		Status:                wagertx.Processed,
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		IdempotencyKey:        "provider-a:transaction-123",
		PayloadHash:           "deadbeef",
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		ResultingBalance:      mustMoney(t, "975.00"),
		CreatedAt:             now,
		UpdatedAt:             now,
	})
	if err != nil {
		t.Fatalf("Rehydrate unexpected error: %v", err)
	}
	balance, ok := tx.ResultingBalance()
	if !ok || !balance.Equal(mustMoney(t, "975.00")) {
		t.Errorf("ResultingBalance() = %s, %v, want 975.00, true", balance, ok)
	}
	if !tx.IsTerminal() {
		t.Errorf("IsTerminal() = false, want true for a rehydrated PROCESSED transaction")
	}
}

func TestRehydrate_RejectsInvalidKindAndStatus(t *testing.T) {
	base := wagertx.RehydrateParams{
		ID:                    uuid.New(),
		WalletID:              uuid.New(),
		PlayerID:              uuid.New(),
		Kind:                  wagertx.Bet,
		Money:                 mustMoney(t, "25.00"),
		Status:                wagertx.Pending,
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		IdempotencyKey:        "provider-a:transaction-123",
		PayloadHash:           "deadbeef",
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		CreatedAt:             time.Now(),
		UpdatedAt:             time.Now(),
	}

	badKind := base
	badKind.Kind = wagertx.Kind("SOMETHING_ELSE")
	if _, err := wagertx.Rehydrate(badKind); !errors.Is(err, wagertx.ErrInvalidKind) {
		t.Errorf("bad kind: error = %v, want ErrInvalidKind", err)
	}

	badStatus := base
	badStatus.Status = wagertx.Status("SOMETHING_ELSE")
	if _, err := wagertx.Rehydrate(badStatus); !errors.Is(err, wagertx.ErrInvalidStatus) {
		t.Errorf("bad status: error = %v, want ErrInvalidStatus", err)
	}
}
