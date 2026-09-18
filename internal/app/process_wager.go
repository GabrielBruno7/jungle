package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"jungle/internal/domain/event"
	"jungle/internal/domain/journal"
	"jungle/internal/domain/money"
	"jungle/internal/domain/wagertx"
	"jungle/internal/domain/wallet"
	"jungle/internal/observability"
)

type ProcessWagerCommand struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    string
	PlayerID                       uuid.UUID
	WalletID                       uuid.UUID
	RoundID                        string
	GameID                         string
	Kind                           wagertx.Kind
	Money                          money.Money
	ReferenceExternalTransactionID string
	CorrelationID                  uuid.UUID

	ConsumerName string
	MessageID    string
}

type ProcessWagerResult struct {
	Transaction wagertx.WagerTransaction

	Balance money.Money

	IdempotentReplay bool
}

type ProcessWager struct {
	uow   UnitOfWork
	repos Repositories
	clock Clock
}

func NewProcessWager(uow UnitOfWork, repos Repositories, clock Clock) *ProcessWager {
	return &ProcessWager{uow: uow, repos: repos, clock: clock}
}

type decisionKind int

const (
	decisionProcess decisionKind = iota
	decisionReject
	decisionPendingReference
)

type decision struct {
	kind       decisionKind
	movesFunds bool
	direction  wallet.LedgerDirection
	failure    wagertx.FailureCode
}

func (uc *ProcessWager) Execute(ctx context.Context, cmd ProcessWagerCommand) (ProcessWagerResult, error) {
	ctx, span := observability.Tracer().Start(ctx, "ProcessWager "+string(cmd.Kind),
		trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()

	span.SetAttributes(observability.OperationAttributes(cmd.ProviderID, cmd.ExternalTransactionID, string(cmd.Kind))...)
	span.SetAttributes(observability.WalletAttributes(cmd.WalletID.String(), cmd.PlayerID.String())...)
	if cmd.MessageID != "" {
		span.SetAttributes(attribute.String("jungle.message_id", cmd.MessageID))
	}

	now := uc.clock.Now()
	var result ProcessWagerResult

	if replay, found, err := uc.fastReplay(ctx, cmd); err != nil {
		return ProcessWagerResult{}, err
	} else if found {
		return replay, nil
	}

	err := uc.uow.Within(ctx, func(ctx context.Context, repos Repositories) error {
		w, err := repos.Wallets().GetForUpdate(ctx, cmd.WalletID)
		if err != nil {
			return err
		}
		if w.PlayerID() != cmd.PlayerID {
			return ErrWalletMismatch
		}

		if cmd.MessageID != "" {
			rec, found, err := repos.Inbox().Get(ctx, cmd.ConsumerName, cmd.MessageID)
			if err != nil {
				return fmt.Errorf("reading inbox: %w", err)
			}
			if found {
				existing, err := repos.Transactions().GetByID(ctx, rec.WagerTransactionID)
				if err != nil {
					return fmt.Errorf("loading transaction for replayed message: %w", err)
				}
				result = replayResult(existing)
				return nil
			}
		}

		existing, err := repos.Transactions().GetByIdempotencyKey(ctx, cmd.IdempotencyKey)
		switch {
		case err == nil:
			if existing.PayloadHash() != cmd.PayloadHash {
				return ErrIdempotencyConflict
			}
			result = replayResult(existing)
			return uc.recordInbox(ctx, repos, cmd, existing.ID(), now)
		case errors.Is(err, ErrNotFound):
		default:
			return fmt.Errorf("reading transaction by idempotency key: %w", err)
		}

		byIdentity, err := repos.Transactions().GetByProviderExternalID(ctx, cmd.ProviderID, cmd.ExternalTransactionID)
		switch {
		case err == nil:
			if byIdentity.IdempotencyKey() != cmd.IdempotencyKey {
				return ErrOperationIdentityConflict
			}
			result = replayResult(byIdentity)
			return uc.recordInbox(ctx, repos, cmd, byIdentity.ID(), now)
		case errors.Is(err, ErrNotFound):
		default:
			return fmt.Errorf("reading transaction by provider identity: %w", err)
		}

		tx, err := wagertx.NewExternal(wagertx.NewExternalParams{
			ID:                             uuid.New(),
			ProviderID:                     cmd.ProviderID,
			ExternalTransactionID:          cmd.ExternalTransactionID,
			IdempotencyKey:                 cmd.IdempotencyKey,
			PayloadHash:                    cmd.PayloadHash,
			WalletID:                       cmd.WalletID,
			PlayerID:                       cmd.PlayerID,
			RoundID:                        cmd.RoundID,
			GameID:                         cmd.GameID,
			Kind:                           cmd.Kind,
			Money:                          cmd.Money,
			ReferenceExternalTransactionID: cmd.ReferenceExternalTransactionID,
			Now:                            now,
		})
		if err != nil {
			return err
		}

		d, err := uc.decide(ctx, repos, &tx, w)
		if err != nil {
			return err
		}

		meta := event.Meta{CorrelationID: cmd.CorrelationID, OccurredAt: now}
		outcome, err := uc.settle(ctx, repos, &tx, w, d, meta, now, repos.Transactions().Insert)
		if err != nil {
			return err
		}

		result = outcome
		return uc.recordInbox(ctx, repos, cmd, tx.ID(), now)
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return ProcessWagerResult{}, err
	}

	span.SetAttributes(observability.OutcomeAttributes(
		result.Transaction.ID().String(),
		string(result.Transaction.Status()),
		string(result.Transaction.FailureCode()),
		result.IdempotentReplay,
	)...)

	return result, nil
}

func (uc *ProcessWager) fastReplay(ctx context.Context, cmd ProcessWagerCommand) (ProcessWagerResult, bool, error) {
	existing, err := uc.repos.Transactions().GetByIdempotencyKey(ctx, cmd.IdempotencyKey)
	switch {
	case errors.Is(err, ErrNotFound):
		return ProcessWagerResult{}, false, nil
	case err != nil:
		return ProcessWagerResult{}, false, fmt.Errorf("reading transaction by idempotency key: %w", err)
	}

	if existing.PayloadHash() != cmd.PayloadHash {
		return ProcessWagerResult{}, false, ErrIdempotencyConflict
	}
	if !existing.IsTerminal() {
		return ProcessWagerResult{}, false, nil
	}

	if cmd.MessageID != "" {
		return ProcessWagerResult{}, false, nil
	}

	return replayResult(existing), true, nil
}

func replayResult(tx wagertx.WagerTransaction) ProcessWagerResult {
	balance, _ := tx.ResultingBalance()
	return ProcessWagerResult{
		Transaction:      tx,
		Balance:          balance,
		IdempotentReplay: true,
	}
}

func (uc *ProcessWager) recordInbox(
	ctx context.Context,
	repos Repositories,
	cmd ProcessWagerCommand,
	transactionID uuid.UUID,
	now time.Time,
) error {
	if cmd.MessageID == "" {
		return nil
	}
	return repos.Inbox().Insert(ctx, InboxRecord{
		ConsumerName:       cmd.ConsumerName,
		MessageID:          cmd.MessageID,
		PayloadHash:        cmd.PayloadHash,
		WagerTransactionID: transactionID,
		ReceivedAt:         now,
		CompletedAt:        now,
	})
}

func (uc *ProcessWager) decide(
	ctx context.Context,
	repos Repositories,
	tx *wagertx.WagerTransaction,
	w wallet.Wallet,
) (decision, error) {
	if tx.Money().Currency() != w.Currency() {
		return decision{kind: decisionReject, failure: wagertx.FailureCurrencyMismatch}, nil
	}

	switch tx.Kind() {
	case wagertx.Bet:
		return decision{kind: decisionProcess, movesFunds: true, direction: wallet.Debit}, nil

	case wagertx.Win:
		uc.attachOptionalReference(ctx, repos, tx)
		return decision{kind: decisionProcess, movesFunds: true, direction: wallet.Credit}, nil

	case wagertx.Loss:
		return decision{kind: decisionProcess, movesFunds: false}, nil

	case wagertx.Refund, wagertx.Rollback:
		return uc.decideReversal(ctx, repos, tx)

	default:
		return decision{}, fmt.Errorf("%w: %s", wagertx.ErrInvalidKind, tx.Kind())
	}
}

func (uc *ProcessWager) attachOptionalReference(ctx context.Context, repos Repositories, tx *wagertx.WagerTransaction) {
	if tx.ReferenceExternalTransactionID() == "" {
		return
	}
	ref, err := repos.Transactions().GetByProviderExternalID(ctx, tx.ProviderID(), tx.ReferenceExternalTransactionID())
	if err != nil {
		return
	}
	_ = tx.ResolveReference(ref.ID())
}

func (uc *ProcessWager) decideReversal(
	ctx context.Context,
	repos Repositories,
	tx *wagertx.WagerTransaction,
) (decision, error) {
	ref, err := repos.Transactions().GetByProviderExternalID(ctx, tx.ProviderID(), tx.ReferenceExternalTransactionID())
	switch {
	case errors.Is(err, ErrNotFound):
		return decision{kind: decisionPendingReference}, nil
	case err != nil:
		return decision{}, fmt.Errorf("resolving reference: %w", err)
	}

	switch ref.Status() {
	case wagertx.Pending, wagertx.PendingReference:
		return decision{kind: decisionPendingReference}, nil
	case wagertx.Rejected, wagertx.Failed:
		return decision{kind: decisionReject, failure: wagertx.FailureReferenceNotSuccessful}, nil
	}

	if ref.PlayerID() != tx.PlayerID() ||
		ref.WalletID() != tx.WalletID() ||
		ref.RoundID() != tx.RoundID() ||
		ref.Money().Currency() != tx.Money().Currency() {
		return decision{kind: decisionReject, failure: wagertx.FailureReferenceMismatch}, nil
	}

	if !ref.Money().Equal(tx.Money()) {
		return decision{kind: decisionReject, failure: wagertx.FailureReferenceMismatch}, nil
	}

	direction, ok := reversalDirection(tx.Kind(), ref.Kind())
	if !ok {
		return decision{kind: decisionReject, failure: wagertx.FailureReferenceMismatch}, nil
	}

	reversed, err := repos.Transactions().HasSuccessfulReversal(ctx, ref.ID())
	if err != nil {
		return decision{}, fmt.Errorf("checking existing reversal: %w", err)
	}
	if reversed {
		return decision{kind: decisionReject, failure: wagertx.FailureDuplicateReversal}, nil
	}

	if err := tx.ResolveReference(ref.ID()); err != nil {
		return decision{}, err
	}

	return decision{kind: decisionProcess, movesFunds: true, direction: direction}, nil
}

func reversalDirection(kind, refKind wagertx.Kind) (wallet.LedgerDirection, bool) {
	if kind == wagertx.Refund {
		if refKind != wagertx.Bet {
			return "", false
		}
		return wallet.Credit, true
	}

	switch refKind {
	case wagertx.Bet:
		return wallet.Credit, true
	case wagertx.Win, wagertx.Refund:
		return wallet.Debit, true
	default:
		return "", false
	}
}

type saveFunc func(context.Context, wagertx.WagerTransaction) error

func (uc *ProcessWager) settle(
	ctx context.Context,
	repos Repositories,
	tx *wagertx.WagerTransaction,
	w wallet.Wallet,
	d decision,
	meta event.Meta,
	now time.Time,
	save saveFunc,
) (ProcessWagerResult, error) {
	switch d.kind {
	case decisionPendingReference:
		if err := tx.MarkPendingReference(now); err != nil {
			return ProcessWagerResult{}, err
		}
		if err := save(ctx, *tx); err != nil {
			return ProcessWagerResult{}, fmt.Errorf("saving pending-reference transaction: %w", err)
		}
		evt, err := event.NewWagerTransactionPendingReference(*tx, meta)
		if err != nil {
			return ProcessWagerResult{}, err
		}
		if err := repos.Outbox().Insert(ctx, evt); err != nil {
			return ProcessWagerResult{}, fmt.Errorf("inserting outbox event: %w", err)
		}
		return ProcessWagerResult{Transaction: *tx, Balance: w.Balance()}, nil

	case decisionReject:
		return uc.reject(ctx, repos, tx, w, d.failure, meta, now, save)

	case decisionProcess:
		if !d.movesFunds {
			if err := tx.MarkProcessed(w.Balance(), now); err != nil {
				return ProcessWagerResult{}, err
			}
			if err := save(ctx, *tx); err != nil {
				return ProcessWagerResult{}, fmt.Errorf("saving transaction: %w", err)
			}
			evt, err := event.NewWagerTransactionProcessed(*tx, w.Balance(), meta)
			if err != nil {
				return ProcessWagerResult{}, err
			}
			if err := repos.Outbox().Insert(ctx, evt); err != nil {
				return ProcessWagerResult{}, fmt.Errorf("inserting outbox event: %w", err)
			}
			return ProcessWagerResult{Transaction: *tx, Balance: w.Balance()}, nil
		}

		balanceBefore := w.Balance()
		expectedVersion := w.Version()

		if err := applyMovement(&w, d.direction, tx.Money(), now); err != nil {
			if errors.Is(err, wallet.ErrInsufficientBalance) {
				code := wagertx.FailureInsufficientBalance
				if tx.Kind().RequiresReference() {
					code = wagertx.FailureInsufficientBalanceForReversal
				}
				return uc.reject(ctx, repos, tx, w, code, meta, now, save)
			}
			return ProcessWagerResult{}, err
		}

		if err := tx.MarkProcessed(w.Balance(), now); err != nil {
			return ProcessWagerResult{}, err
		}
		if err := save(ctx, *tx); err != nil {
			return ProcessWagerResult{}, fmt.Errorf("saving transaction: %w", err)
		}

		if err := repos.Wallets().UpdateBalance(ctx, w, expectedVersion); err != nil {
			return ProcessWagerResult{}, err
		}

		entry, err := wallet.NewWalletLedgerEntry(wallet.NewWalletLedgerEntryParams{
			ID:            uuid.New(),
			WalletID:      w.ID(),
			TransactionID: tx.ID(),
			Direction:     d.direction,
			Amount:        tx.Money(),
			BalanceBefore: balanceBefore,
			Now:           now,
		})
		if err != nil {
			return ProcessWagerResult{}, err
		}
		if err := repos.Ledger().Insert(ctx, entry); err != nil {
			return ProcessWagerResult{}, fmt.Errorf("inserting ledger entry: %w", err)
		}

		if err := recordDoubleEntry(ctx, repos, *tx, d.direction, now); err != nil {
			return ProcessWagerResult{}, err
		}

		processed, err := event.NewWagerTransactionProcessed(*tx, w.Balance(), meta)
		if err != nil {
			return ProcessWagerResult{}, err
		}
		causedBy := tx.ID()
		balanceMeta := meta
		balanceMeta.CausationID = &causedBy

		changed, err := event.NewWalletBalanceChanged(entry, w.Version(), balanceMeta)
		if err != nil {
			return ProcessWagerResult{}, err
		}
		if err := repos.Outbox().Insert(ctx, processed, changed); err != nil {
			return ProcessWagerResult{}, fmt.Errorf("inserting outbox events: %w", err)
		}

		return ProcessWagerResult{Transaction: *tx, Balance: w.Balance()}, nil

	default:
		return ProcessWagerResult{}, fmt.Errorf("unhandled decision %d", d.kind)
	}
}

func (uc *ProcessWager) reject(
	ctx context.Context,
	repos Repositories,
	tx *wagertx.WagerTransaction,
	w wallet.Wallet,
	code wagertx.FailureCode,
	meta event.Meta,
	now time.Time,
	save saveFunc,
) (ProcessWagerResult, error) {
	if err := tx.MarkRejected(code, now); err != nil {
		return ProcessWagerResult{}, err
	}
	if err := save(ctx, *tx); err != nil {
		return ProcessWagerResult{}, fmt.Errorf("saving rejected transaction: %w", err)
	}
	evt, err := event.NewWagerTransactionRejected(*tx, meta)
	if err != nil {
		return ProcessWagerResult{}, err
	}
	if err := repos.Outbox().Insert(ctx, evt); err != nil {
		return ProcessWagerResult{}, fmt.Errorf("inserting outbox event: %w", err)
	}
	return ProcessWagerResult{Transaction: *tx, Balance: w.Balance()}, nil
}

func applyMovement(w *wallet.Wallet, direction wallet.LedgerDirection, amount money.Money, now time.Time) error {
	switch direction {
	case wallet.Debit:
		return w.Debit(amount, now)
	case wallet.Credit:
		return w.Credit(amount, now)
	default:
		return fmt.Errorf("%w: %s", wallet.ErrInvalidDirection, direction)
	}
}

func recordDoubleEntry(ctx context.Context, repos Repositories, tx wagertx.WagerTransaction, direction wallet.LedgerDirection, now time.Time) error {
	walletAccount := journal.WalletAccount(tx.WalletID())
	counterparty := journal.ProviderAccount(tx.ProviderID())

	from, to := counterparty, walletAccount
	if direction == wallet.Debit {
		from, to = walletAccount, counterparty
	}

	entries, err := journal.NewMovement(journal.MovementParams{
		TransactionID: tx.ID(),
		From:          from,
		To:            to,
		Amount:        tx.Money(),
		Now:           now,
	})
	if err != nil {
		return fmt.Errorf("building journal entries: %w", err)
	}
	if err := journal.AssertBalanced(entries); err != nil {
		return err
	}
	if err := repos.Journal().Insert(ctx, entries...); err != nil {
		return fmt.Errorf("inserting journal entries: %w", err)
	}
	return nil
}
