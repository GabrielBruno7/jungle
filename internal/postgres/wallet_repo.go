package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"jungle/internal/app"
	"jungle/internal/domain/money"
	"jungle/internal/domain/wallet"
)

type walletRepo struct {
	q Querier
}

const walletColumns = `id, player_id, currency, balance_minor_units, version, created_at, updated_at`

func (r *walletRepo) Insert(ctx context.Context, w wallet.Wallet) error {
	const query = `
		INSERT INTO wallet (id, player_id, currency, balance_minor_units, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	_, err := r.q.Exec(ctx, query,
		w.ID(), w.PlayerID(), string(w.Currency()),
		w.Balance().MinorUnits(), w.Version(),
		w.CreatedAt(), w.UpdatedAt(),
	)
	if err != nil {
		if name, ok := constraintViolation(err); ok && name == "wallet_player_currency_unique" {
			return app.ErrWalletAlreadyExists
		}
		return fmt.Errorf("inserting wallet: %w", err)
	}
	return nil
}

func (r *walletRepo) Get(ctx context.Context, id uuid.UUID) (wallet.Wallet, error) {
	return r.get(ctx, id, false)
}

func (r *walletRepo) GetForUpdate(ctx context.Context, id uuid.UUID) (wallet.Wallet, error) {
	return r.get(ctx, id, true)
}

func (r *walletRepo) get(ctx context.Context, id uuid.UUID, lock bool) (wallet.Wallet, error) {
	query := `SELECT ` + walletColumns + ` FROM wallet WHERE id = $1`
	if lock {
		query += ` FOR UPDATE`
	}

	var (
		walletID  uuid.UUID
		playerID  uuid.UUID
		currency  string
		balance   int64
		version   int64
		createdAt time.Time
		updatedAt time.Time
	)

	err := r.q.QueryRow(ctx, query, id).Scan(
		&walletID, &playerID, &currency, &balance, &version, &createdAt, &updatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return wallet.Wallet{}, app.ErrNotFound
	}
	if err != nil {
		return wallet.Wallet{}, fmt.Errorf("selecting wallet: %w", err)
	}

	cur := money.Currency(currency)
	return wallet.RehydrateWallet(wallet.RehydrateWalletParams{
		ID:        walletID,
		PlayerID:  playerID,
		Currency:  cur,
		Balance:   money.FromMinorUnits(balance, cur),
		Version:   version,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	})
}

func (r *walletRepo) UpdateBalance(ctx context.Context, w wallet.Wallet, expectedVersion int64) error {
	const query = `
		UPDATE wallet
		SET balance_minor_units = $1, version = $2, updated_at = $3
		WHERE id = $4 AND version = $5`

	tag, err := r.q.Exec(ctx, query,
		w.Balance().MinorUnits(), w.Version(), w.UpdatedAt(),
		w.ID(), expectedVersion,
	)
	if err != nil {
		return fmt.Errorf("updating wallet balance: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return app.ErrConcurrencyConflict
	}
	return nil
}

func (r *walletRepo) ExistsForPlayerCurrency(ctx context.Context, playerID uuid.UUID, currency money.Currency) (bool, error) {
	const query = `SELECT EXISTS (SELECT 1 FROM wallet WHERE player_id = $1 AND currency = $2)`

	var exists bool
	if err := r.q.QueryRow(ctx, query, playerID, string(currency)).Scan(&exists); err != nil {
		return false, fmt.Errorf("checking wallet existence: %w", err)
	}
	return exists, nil
}
