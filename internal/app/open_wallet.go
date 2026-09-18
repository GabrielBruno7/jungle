package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"jungle/internal/domain/event"
	"jungle/internal/domain/journal"
	"jungle/internal/domain/money"
	"jungle/internal/domain/wagertx"
	"jungle/internal/domain/wallet"
)

type OpenWalletCommand struct {
	PlayerID       uuid.UUID
	InitialBalance money.Money
	CorrelationID  uuid.UUID
}

type OpenWalletResult struct {
	Wallet wallet.Wallet
}

type OpenWallet struct {
	uow   UnitOfWork
	clock Clock
}

func NewOpenWallet(uow UnitOfWork, clock Clock) *OpenWallet {
	return &OpenWallet{uow: uow, clock: clock}
}

func (uc *OpenWallet) Execute(ctx context.Context, cmd OpenWalletCommand) (OpenWalletResult, error) {
	now := uc.clock.Now()
	currency := cmd.InitialBalance.Currency()

	var created wallet.Wallet

	err := uc.uow.Within(ctx, func(ctx context.Context, repos Repositories) error {
		exists, err := repos.Wallets().ExistsForPlayerCurrency(ctx, cmd.PlayerID, currency)
		if err != nil {
			return fmt.Errorf("checking existing wallet: %w", err)
		}
		if exists {
			return ErrWalletAlreadyExists
		}

		w, err := wallet.NewWallet(wallet.NewWalletParams{
			ID:             uuid.New(),
			PlayerID:       cmd.PlayerID,
			Currency:       currency,
			InitialBalance: cmd.InitialBalance,
			Now:            now,
		})
		if err != nil {
			return fmt.Errorf("building wallet: %w", err)
		}

		if err := repos.Wallets().Insert(ctx, w); err != nil {
			if errors.Is(err, ErrWalletAlreadyExists) {
				return ErrWalletAlreadyExists
			}
			return fmt.Errorf("inserting wallet: %w", err)
		}

		created = w

		if cmd.InitialBalance.IsZero() {
			return nil
		}

		return uc.recordOpeningCredit(ctx, repos, w, cmd.CorrelationID, now)
	})
	if err != nil {
		return OpenWalletResult{}, err
	}

	return OpenWalletResult{Wallet: created}, nil
}

func (uc *OpenWallet) recordOpeningCredit(
	ctx context.Context,
	repos Repositories,
	w wallet.Wallet,
	correlationID uuid.UUID,
	now time.Time,
) error {
	opening, err := wagertx.NewOpening(wagertx.NewOpeningParams{
		ID:       uuid.New(),
		WalletID: w.ID(),
		PlayerID: w.PlayerID(),
		Money:    w.Balance(),
		Now:      now,
	})
	if err != nil {
		return fmt.Errorf("building opening transaction: %w", err)
	}
	if err := repos.Transactions().Insert(ctx, opening); err != nil {
		return fmt.Errorf("inserting opening transaction: %w", err)
	}

	entry, err := wallet.NewWalletLedgerEntry(wallet.NewWalletLedgerEntryParams{
		ID:            uuid.New(),
		WalletID:      w.ID(),
		TransactionID: opening.ID(),
		Direction:     wallet.Credit,
		Amount:        w.Balance(),
		BalanceBefore: money.Zero(w.Currency()),
		Now:           now,
	})
	if err != nil {
		return fmt.Errorf("building opening ledger entry: %w", err)
	}
	if err := repos.Ledger().Insert(ctx, entry); err != nil {
		return fmt.Errorf("inserting opening ledger entry: %w", err)
	}

	journalEntries, err := journal.NewMovement(journal.MovementParams{
		TransactionID: opening.ID(),
		From:          journal.PlatformFunding,
		To:            journal.WalletAccount(w.ID()),
		Amount:        w.Balance(),
		Now:           now,
	})
	if err != nil {
		return fmt.Errorf("building opening journal entries: %w", err)
	}
	if err := repos.Journal().Insert(ctx, journalEntries...); err != nil {
		return fmt.Errorf("inserting opening journal entries: %w", err)
	}

	meta := event.Meta{CorrelationID: correlationID, OccurredAt: now}

	processed, err := event.NewWagerTransactionProcessed(opening, w.Balance(), meta)
	if err != nil {
		return fmt.Errorf("building processed event: %w", err)
	}
	causedBy := opening.ID()
	balanceMeta := meta
	balanceMeta.CausationID = &causedBy

	balanceChanged, err := event.NewWalletBalanceChanged(entry, w.Version(), balanceMeta)
	if err != nil {
		return fmt.Errorf("building balance changed event: %w", err)
	}

	if err := repos.Outbox().Insert(ctx, processed, balanceChanged); err != nil {
		return fmt.Errorf("inserting outbox events: %w", err)
	}
	return nil
}
