package wagertx

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"jungle/internal/domain/money"
)

var (
	ErrInvalidTransaction     = errors.New("wagertx: invalid transaction")
	ErrInvalidKind            = errors.New("wagertx: invalid kind")
	ErrInvalidStatus          = errors.New("wagertx: invalid status")
	ErrInvalidTransition      = errors.New("wagertx: invalid state transition")
	ErrTerminalState          = errors.New("wagertx: transaction is already in a terminal state")
	ErrOpeningNotExternal     = errors.New("wagertx: OPENING is internal-only and cannot be submitted externally")
	ErrReferenceRequired      = errors.New("wagertx: referenceExternalTransactionId is required for this kind")
	ErrReferenceNotApplicable = errors.New("wagertx: referenceExternalTransactionId is not applicable for this kind")
	ErrInvalidAmountForKind   = errors.New("wagertx: amount is not valid for this kind")
)

// WagerTransaction is the record of one operation on a wallet: either an
// external one a provider submitted (BET, WIN, LOSS, REFUND, ROLLBACK) or
// the internal OPENING credit issued when a wallet is created with a
// positive initial balance.
//
// It starts PENDING (external) or already PROCESSED (OPENING, applied
// synchronously) and moves through the transitions in status.go; once it
// reaches a terminal status (PROCESSED, REJECTED, FAILED) it never
// transitions again — a replay just reads the persisted result back.
type WagerTransaction struct {
	id       uuid.UUID
	walletID uuid.UUID
	playerID uuid.UUID
	kind     Kind
	money    money.Money
	status   Status

	// External-only metadata. Zero-valued for OPENING.
	providerID             string
	externalTransactionID  string
	idempotencyKey         string
	payloadHash            string
	roundID                string
	gameID                 string
	referenceExternalTxID  string
	referenceTransactionID uuid.UUID

	// Populated once terminal.
	failureCode         FailureCode
	resultingBalance    money.Money
	hasResultingBalance bool

	createdAt time.Time
	updatedAt time.Time
}

// NewOpeningParams carries the arguments for the internal wallet-opening
// credit.
type NewOpeningParams struct {
	ID       uuid.UUID
	WalletID uuid.UUID
	PlayerID uuid.UUID
	Money    money.Money
	Now      time.Time
}

// NewOpening creates the OPENING transaction for a wallet opened with a
// positive initial balance. It is applied synchronously — there is no
// PENDING phase, no external dependency to wait on — so it is created
// already PROCESSED, with its resulting balance equal to the credited
// amount (the wallet started at zero).
//
// Callers must never call this for a zero initial balance: per the
// challenge, that case creates no OPENING, no ledger entry, and no events
// at all, so Money must be strictly positive here.
func NewOpening(p NewOpeningParams) (WagerTransaction, error) {
	if p.ID == uuid.Nil || p.WalletID == uuid.Nil || p.PlayerID == uuid.Nil {
		return WagerTransaction{}, fmt.Errorf("%w: missing identity fields", ErrInvalidTransaction)
	}
	if !p.Money.IsPositive() {
		return WagerTransaction{}, fmt.Errorf("%w: OPENING requires a positive amount", ErrInvalidAmountForKind)
	}
	if p.Now.IsZero() {
		return WagerTransaction{}, fmt.Errorf("%w: now is required", ErrInvalidTransaction)
	}

	return WagerTransaction{
		id:                  p.ID,
		walletID:            p.WalletID,
		playerID:            p.PlayerID,
		kind:                Opening,
		money:               p.Money,
		status:              Processed,
		resultingBalance:    p.Money,
		hasResultingBalance: true,
		createdAt:           p.Now,
		updatedAt:           p.Now,
	}, nil
}

// NewExternalParams carries the arguments for a transaction submitted by a
// provider, via HTTP or SQS.
type NewExternalParams struct {
	ID                             uuid.UUID
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    string
	WalletID                       uuid.UUID
	PlayerID                       uuid.UUID
	RoundID                        string
	GameID                         string
	Kind                           Kind
	Money                          money.Money
	ReferenceExternalTransactionID string
	Now                            time.Time
}

// NewExternal validates and creates a transaction submitted by a provider.
// It always starts PENDING. OPENING is rejected outright — providers can
// never submit it.
func NewExternal(p NewExternalParams) (WagerTransaction, error) {
	if p.Kind == Opening {
		return WagerTransaction{}, ErrOpeningNotExternal
	}
	if !p.Kind.IsValidExternal() {
		return WagerTransaction{}, fmt.Errorf("%w: %q", ErrInvalidKind, p.Kind)
	}
	if p.ID == uuid.Nil || p.WalletID == uuid.Nil || p.PlayerID == uuid.Nil {
		return WagerTransaction{}, fmt.Errorf("%w: missing identity fields", ErrInvalidTransaction)
	}
	if p.ProviderID == "" || p.ExternalTransactionID == "" || p.IdempotencyKey == "" ||
		p.PayloadHash == "" || p.RoundID == "" || p.GameID == "" {
		return WagerTransaction{}, fmt.Errorf("%w: missing required external metadata", ErrInvalidTransaction)
	}
	if p.Now.IsZero() {
		return WagerTransaction{}, fmt.Errorf("%w: now is required", ErrInvalidTransaction)
	}

	switch p.Kind {
	case Bet, Win, Refund, Rollback:
		if !p.Money.IsPositive() {
			return WagerTransaction{}, fmt.Errorf("%w: %s requires a positive amount", ErrInvalidAmountForKind, p.Kind)
		}
	case Loss:
		if !p.Money.IsZero() {
			return WagerTransaction{}, fmt.Errorf("%w: LOSS requires amount 0.00", ErrInvalidAmountForKind)
		}
	}

	if p.Kind.RequiresReference() && p.ReferenceExternalTransactionID == "" {
		return WagerTransaction{}, fmt.Errorf("%w: %s", ErrReferenceRequired, p.Kind)
	}
	if !p.Kind.AllowsReference() && p.ReferenceExternalTransactionID != "" {
		return WagerTransaction{}, fmt.Errorf("%w: %s", ErrReferenceNotApplicable, p.Kind)
	}

	return WagerTransaction{
		id:                    p.ID,
		walletID:              p.WalletID,
		playerID:              p.PlayerID,
		kind:                  p.Kind,
		money:                 p.Money,
		status:                Pending,
		providerID:            p.ProviderID,
		externalTransactionID: p.ExternalTransactionID,
		idempotencyKey:        p.IdempotencyKey,
		payloadHash:           p.PayloadHash,
		roundID:               p.RoundID,
		gameID:                p.GameID,
		referenceExternalTxID: p.ReferenceExternalTransactionID,
		createdAt:             p.Now,
		updatedAt:             p.Now,
	}, nil
}

// RehydrateParams carries the exact persisted state of a transaction,
// external or internal.
type RehydrateParams struct {
	ID                             uuid.UUID
	WalletID                       uuid.UUID
	PlayerID                       uuid.UUID
	Kind                           Kind
	Money                          money.Money
	Status                         Status
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    string
	RoundID                        string
	GameID                         string
	ReferenceExternalTransactionID string
	ReferenceTransactionID         uuid.UUID
	FailureCode                    FailureCode
	ResultingBalance               money.Money
	CreatedAt                      time.Time
	UpdatedAt                      time.Time
}

// Rehydrate reconstructs a WagerTransaction from already-persisted state.
// It checks structural validity (recognized kind/status, identity fields
// present) but never re-runs any transition, re-derives a result, or
// re-applies a movement — the transitions already happened and were
// committed; this just loads the current snapshot.
func Rehydrate(p RehydrateParams) (WagerTransaction, error) {
	if p.ID == uuid.Nil || p.WalletID == uuid.Nil || p.PlayerID == uuid.Nil {
		return WagerTransaction{}, fmt.Errorf("%w: missing identity fields", ErrInvalidTransaction)
	}
	if !p.Kind.IsValid() {
		return WagerTransaction{}, fmt.Errorf("%w: %q", ErrInvalidKind, p.Kind)
	}
	if !p.Status.IsValid() {
		return WagerTransaction{}, fmt.Errorf("%w: %q", ErrInvalidStatus, p.Status)
	}

	return WagerTransaction{
		id:                     p.ID,
		walletID:               p.WalletID,
		playerID:               p.PlayerID,
		kind:                   p.Kind,
		money:                  p.Money,
		status:                 p.Status,
		providerID:             p.ProviderID,
		externalTransactionID:  p.ExternalTransactionID,
		idempotencyKey:         p.IdempotencyKey,
		payloadHash:            p.PayloadHash,
		roundID:                p.RoundID,
		gameID:                 p.GameID,
		referenceExternalTxID:  p.ReferenceExternalTransactionID,
		referenceTransactionID: p.ReferenceTransactionID,
		failureCode:            p.FailureCode,
		resultingBalance:       p.ResultingBalance,
		hasResultingBalance:    p.Status == Processed,
		createdAt:              p.CreatedAt,
		updatedAt:              p.UpdatedAt,
	}, nil
}

// --- accessors ---

func (t WagerTransaction) ID() uuid.UUID                          { return t.id }
func (t WagerTransaction) WalletID() uuid.UUID                    { return t.walletID }
func (t WagerTransaction) PlayerID() uuid.UUID                    { return t.playerID }
func (t WagerTransaction) Kind() Kind                             { return t.kind }
func (t WagerTransaction) Money() money.Money                     { return t.money }
func (t WagerTransaction) Status() Status                         { return t.status }
func (t WagerTransaction) ProviderID() string                     { return t.providerID }
func (t WagerTransaction) ExternalTransactionID() string          { return t.externalTransactionID }
func (t WagerTransaction) IdempotencyKey() string                 { return t.idempotencyKey }
func (t WagerTransaction) PayloadHash() string                    { return t.payloadHash }
func (t WagerTransaction) RoundID() string                        { return t.roundID }
func (t WagerTransaction) GameID() string                         { return t.gameID }
func (t WagerTransaction) ReferenceExternalTransactionID() string { return t.referenceExternalTxID }
func (t WagerTransaction) ReferenceTransactionID() uuid.UUID      { return t.referenceTransactionID }
func (t WagerTransaction) FailureCode() FailureCode               { return t.failureCode }
func (t WagerTransaction) CreatedAt() time.Time                   { return t.createdAt }
func (t WagerTransaction) UpdatedAt() time.Time                   { return t.updatedAt }

// ResultingBalance returns the wallet balance observed right after this
// transaction was applied, and whether one has been recorded at all (only
// ever true once the transaction is PROCESSED) — a replay returns this
// exact snapshot rather than the wallet's current, possibly-since-changed
// balance.
func (t WagerTransaction) ResultingBalance() (money.Money, bool) {
	return t.resultingBalance, t.hasResultingBalance
}

// IsTerminal reports whether this transaction has reached PROCESSED,
// REJECTED, or FAILED and can never transition again.
func (t WagerTransaction) IsTerminal() bool {
	return t.status.IsTerminal()
}

func (t *WagerTransaction) transitionTo(next Status, now time.Time) error {
	allowed := allowedTransitions[t.status]
	if !allowed[next] {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, t.status, next)
	}
	t.status = next
	t.updatedAt = now
	return nil
}

// MarkProcessed transitions the transaction to PROCESSED — its terminal
// success status — recording the wallet balance observed right after this
// movement was applied.
func (t *WagerTransaction) MarkProcessed(resultingBalance money.Money, now time.Time) error {
	if err := t.transitionTo(Processed, now); err != nil {
		return err
	}
	t.resultingBalance = resultingBalance
	t.hasResultingBalance = true
	return nil
}

// MarkRejected transitions the transaction to REJECTED — its terminal
// business-rule-failure status — with a stable, required failure code.
func (t *WagerTransaction) MarkRejected(code FailureCode, now time.Time) error {
	if code == "" {
		return fmt.Errorf("%w: failure code is required", ErrInvalidTransaction)
	}
	if err := t.transitionTo(Rejected, now); err != nil {
		return err
	}
	t.failureCode = code
	return nil
}

// MarkFailed transitions the transaction to FAILED — its terminal
// permanent-infrastructure-failure status, kept for audit — with a stable,
// required failure code.
func (t *WagerTransaction) MarkFailed(code FailureCode, now time.Time) error {
	if code == "" {
		return fmt.Errorf("%w: failure code is required", ErrInvalidTransaction)
	}
	if err := t.transitionTo(Failed, now); err != nil {
		return err
	}
	t.failureCode = code
	return nil
}

// MarkPendingReference transitions the transaction to PENDING_REFERENCE:
// its reversal target has not arrived yet. Only REFUND and ROLLBACK ever
// have a reference to wait for.
func (t *WagerTransaction) MarkPendingReference(now time.Time) error {
	if !t.kind.RequiresReference() {
		return fmt.Errorf("%w: only REFUND and ROLLBACK can be PENDING_REFERENCE", ErrInvalidTransition)
	}
	return t.transitionTo(PendingReference, now)
}

// ResolveReference records the internal transaction that
// referenceExternalTransactionId resolved to. It does not itself change
// status — the caller still decides, afterwards, whether to
// MarkProcessed, MarkRejected, or MarkFailed based on what the resolved
// reference turned out to be.
func (t *WagerTransaction) ResolveReference(referenceTransactionID uuid.UUID) error {
	if t.IsTerminal() {
		return fmt.Errorf("%w: cannot resolve reference on a terminal transaction", ErrTerminalState)
	}
	if referenceTransactionID == uuid.Nil {
		return fmt.Errorf("%w: referenceTransactionId is required", ErrInvalidTransaction)
	}
	t.referenceTransactionID = referenceTransactionID
	return nil
}
