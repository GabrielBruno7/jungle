package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"go.uber.org/zap"

	"jungle/internal/config"
)

// NewPool builds a pgx connection pool. The actual connection is opened
// (and verified) on OnStart, and the pool is closed on OnStop.
func NewPool(lc fx.Lifecycle, cfg *config.Config, logger *zap.Logger) (*pgxpool.Pool, error) {
	dsn := fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=%s",
		cfg.Postgres.User, cfg.Postgres.Password,
		cfg.Postgres.Host, cfg.Postgres.Port,
		cfg.Postgres.DBName, cfg.Postgres.SSLMode,
	)

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return nil, fmt.Errorf("creating postgres pool: %w", err)
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := pool.Ping(ctx); err != nil {
				return fmt.Errorf("pinging postgres: %w", err)
			}
			logger.Info("connected to postgres",
				zap.String("host", cfg.Postgres.Host),
				zap.Int("port", cfg.Postgres.Port),
				zap.String("db", cfg.Postgres.DBName),
			)
			return nil
		},
		OnStop: func(context.Context) error {
			logger.Info("closing postgres pool")
			pool.Close()
			return nil
		},
	})

	return pool, nil
}

// Module provides the shared *pgxpool.Pool and forces it to connect on
// startup even before any feature depends on it.
var Module = fx.Module("postgres",
	fx.Provide(NewPool),
	fx.Invoke(func(*pgxpool.Pool) {}),
)
