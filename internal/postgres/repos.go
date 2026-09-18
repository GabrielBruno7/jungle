package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"jungle/internal/app"
)

type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var (
	_ app.Repositories = (*Repos)(nil)
	_ app.UnitOfWork   = (*UnitOfWork)(nil)
)

type Repos struct {
	q Querier
}

func NewRepos(q Querier) *Repos { return &Repos{q: q} }

func (r *Repos) Wallets() app.WalletRepository           { return &walletRepo{q: r.q} }
func (r *Repos) Transactions() app.TransactionRepository { return &transactionRepo{q: r.q} }
func (r *Repos) Ledger() app.LedgerRepository            { return &ledgerRepo{q: r.q} }
func (r *Repos) Inbox() app.InboxRepository              { return &inboxRepo{q: r.q} }
func (r *Repos) Outbox() app.OutboxRepository            { return &outboxRepo{q: r.q} }
func (r *Repos) Journal() app.JournalRepository          { return &journalRepo{q: r.q} }

type UnitOfWork struct {
	pool *pgxpool.Pool
}

func NewUnitOfWork(pool *pgxpool.Pool) *UnitOfWork { return &UnitOfWork{pool: pool} }

func (u *UnitOfWork) Within(ctx context.Context, fn func(context.Context, app.Repositories) error) error {
	tx, err := u.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	if err := fn(ctx, NewRepos(tx)); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}
	committed = true
	return nil
}

const uniqueViolation = "23505"

func constraintViolation(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return pgErr.ConstraintName, true
	}
	return "", false
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
