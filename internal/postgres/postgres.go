package postgres

import (
	"context"
	"fmt"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"
	"go.uber.org/zap"

	"jungle/internal/app"
	"jungle/internal/config"
)

func NewPool(lc fx.Lifecycle, cfg *config.Config, logger *zap.Logger, tracerProvider trace.TracerProvider) (*pgxpool.Pool, error) {
	dsn := fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=%s",
		cfg.Postgres.User, cfg.Postgres.Password,
		cfg.Postgres.Host, cfg.Postgres.Port,
		cfg.Postgres.DBName, cfg.Postgres.SSLMode,
	)

	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parsing postgres dsn: %w", err)
	}
	poolCfg.ConnConfig.Tracer = otelpgx.NewTracer(
		otelpgx.WithTracerProvider(tracerProvider),
		otelpgx.WithTrimSQLInSpanName(),
	)

	pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
	if err != nil {
		return nil, fmt.Errorf("creating postgres pool: %w", err)
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := waitFor(ctx, cfg.Startup.DependencyTimeout, func(ctx context.Context) error {
				return pool.Ping(ctx)
			}, func(attempt int, err error) {
				logger.Warn("postgres not reachable yet, retrying",
					zap.String("host", cfg.Postgres.Host),
					zap.Int("attempt", attempt),
					zap.Error(err))
			}); err != nil {
				return fmt.Errorf("connecting to postgres: %w", err)
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

func NewPoolRepos(pool *pgxpool.Pool) *Repos {
	return NewRepos(pool)
}

var Module = fx.Module("postgres",
	fx.Provide(
		NewPool,
		fx.Annotate(NewUnitOfWork, fx.As(new(app.UnitOfWork))),
		fx.Annotate(NewPoolRepos, fx.As(new(app.Repositories))),
	),
	fx.Invoke(func(*pgxpool.Pool) {}),
)
