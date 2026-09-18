package app

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"jungle/internal/domain/money"
	"jungle/internal/domain/wagertx"
	"jungle/internal/domain/wallet"
)

type Queries struct {
	repos Repositories
	uow   UnitOfWork
}

func NewQueries(repos Repositories, uow UnitOfWork) *Queries {
	return &Queries{repos: repos, uow: uow}
}

func (q *Queries) GetWallet(ctx context.Context, id Identity, walletID uuid.UUID) (wallet.Wallet, error) {
	if !id.MayUseWalletOperations() {
		return wallet.Wallet{}, ErrForbidden
	}
	return q.repos.Wallets().Get(ctx, walletID)
}

type LedgerPage struct {
	Entries    []wallet.WalletLedgerEntry
	NextCursor string
}

func (q *Queries) GetLedger(ctx context.Context, id Identity, walletID uuid.UUID, cursor string, limit int) (LedgerPage, error) {
	if !id.MayUseWalletOperations() {
		return LedgerPage{}, ErrForbidden
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	entries, next, err := q.repos.Ledger().Page(ctx, walletID, cursor, limit)
	if err != nil {
		return LedgerPage{}, err
	}
	return LedgerPage{Entries: entries, NextCursor: next}, nil
}

func (q *Queries) GetTransaction(ctx context.Context, id Identity, transactionID uuid.UUID) (wagertx.WagerTransaction, error) {
	tx, err := q.repos.Transactions().GetByID(ctx, transactionID)
	if err != nil {
		return wagertx.WagerTransaction{}, err
	}
	if !id.MayActAsProvider(tx.ProviderID()) {
		return wagertx.WagerTransaction{}, ErrNotFound
	}
	return tx, nil
}

func (q *Queries) GetProviderTransaction(ctx context.Context, id Identity, providerID, externalID string) (wagertx.WagerTransaction, error) {
	if !id.MayActAsProvider(providerID) {
		return wagertx.WagerTransaction{}, ErrNotFound
	}
	return q.repos.Transactions().GetByProviderExternalID(ctx, providerID, externalID)
}

type ReconcileResult struct {
	WalletID          uuid.UUID
	StoredBalance     money.Money
	CalculatedBalance money.Money
	Difference        money.Money
	Consistent        bool
	CheckedEntries    int
}

func (q *Queries) Reconcile(ctx context.Context, id Identity, walletID uuid.UUID) (ReconcileResult, error) {
	if !id.MayUseWalletOperations() {
		return ReconcileResult{}, ErrForbidden
	}

	var result ReconcileResult

	err := q.uow.Within(ctx, func(ctx context.Context, repos Repositories) error {
		w, err := repos.Wallets().Get(ctx, walletID)
		if err != nil {
			return err
		}

		credits, debits, entries, err := repos.Ledger().Totals(ctx, walletID)
		if err != nil {
			return fmt.Errorf("summing ledger: %w", err)
		}

		calculated := money.FromMinorUnits(credits-debits, w.Currency())
		difference, err := w.Balance().Sub(calculated)
		if err != nil {
			return fmt.Errorf("computing difference: %w", err)
		}

		result = ReconcileResult{
			WalletID:          walletID,
			StoredBalance:     w.Balance(),
			CalculatedBalance: calculated,
			Difference:        difference,
			Consistent:        difference.IsZero(),
			CheckedEntries:    entries,
		}
		return nil
	})
	if err != nil {
		return ReconcileResult{}, err
	}

	return result, nil
}
