package worker

import (
	"context"
	"time"

	"go.uber.org/fx"
	"go.uber.org/zap"

	"jungle/internal/app"
	"jungle/internal/config"
	"jungle/internal/queue"
)

type Lifetime struct {
	Fetch context.Context
	Work  context.Context
}

type runner struct {
	name        string
	stopFetch   context.CancelFunc
	abandonWork context.CancelFunc
	done        chan struct{}
}

func start(lc fx.Lifecycle, logger *zap.Logger, shutdownTimeout time.Duration, name string, loop func(Lifetime)) {
	r := &runner{name: name, done: make(chan struct{})}

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			fetchCtx, stopFetch := context.WithCancel(context.Background())
			workCtx, abandonWork := context.WithCancel(context.Background())
			r.stopFetch, r.abandonWork = stopFetch, abandonWork

			go func() {
				defer close(r.done)
				logger.Info("worker started", zap.String("worker", name))
				loop(Lifetime{Fetch: fetchCtx, Work: workCtx})
				logger.Info("worker stopped", zap.String("worker", name))
			}()
			return nil
		},

		OnStop: func(ctx context.Context) error {
			if r.stopFetch == nil {
				return nil
			}

			logger.Info("worker draining", zap.String("worker", name))
			r.stopFetch()
			defer r.abandonWork()

			deadline := time.NewTimer(shutdownTimeout)
			defer deadline.Stop()

			select {
			case <-r.done:
				logger.Info("worker drained", zap.String("worker", name))
				return nil
			case <-deadline.C:
				logger.Warn("worker exceeded the shutdown budget; abandoning uncommitted work",
					zap.String("worker", name),
					zap.Duration("budget", shutdownTimeout))
				return nil
			case <-ctx.Done():
				logger.Warn("shutdown cancelled before the worker drained", zap.String("worker", name))
				return nil
			}
		},
	})
}

func tick(life Lifetime, interval time.Duration, fn func(context.Context)) {
	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		select {
		case <-life.Fetch.Done():
			return
		case <-timer.C:
			fn(life.Work)
			timer.Reset(interval)
		}
	}
}

func RegisterConsumer(lc fx.Lifecycle, consumer *queue.Consumer, cfg *config.Config, logger *zap.Logger) {
	start(lc, logger, cfg.Worker.ShutdownTimeout, "sqs-consumer", func(life Lifetime) {
		consumer.Run(queue.Lifetime{Fetch: life.Fetch, Work: life.Work})
	})
}

func RegisterOutboxPublisher(lc fx.Lifecycle, publisher *app.PublishOutbox, cfg *config.Config, logger *zap.Logger) {
	start(lc, logger, cfg.Worker.ShutdownTimeout, "outbox-publisher", func(life Lifetime) {
		tick(life, cfg.Worker.OutboxInterval, func(ctx context.Context) {
			if _, err := publisher.RunOnce(ctx); err != nil && ctx.Err() == nil {
				logger.Warn("outbox publish pass failed", zap.Error(err))
			}
		})
	})
}

func RegisterReferenceResolver(lc fx.Lifecycle, resolver *app.ResolveReferences, cfg *config.Config, logger *zap.Logger) {
	start(lc, logger, cfg.Worker.ShutdownTimeout, "reference-resolver", func(life Lifetime) {
		tick(life, cfg.Worker.ReferenceInterval, func(ctx context.Context) {
			if _, err := resolver.RunOnce(ctx); err != nil && ctx.Err() == nil {
				logger.Warn("reference resolution pass failed", zap.Error(err))
			}
		})
	})
}

var Module = fx.Module("worker",
	fx.Invoke(
		RegisterConsumer,
		RegisterOutboxPublisher,
		RegisterReferenceResolver,
	),
)
