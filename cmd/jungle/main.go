package main

import (
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
	"go.uber.org/zap"

	"jungle/internal/config"
	"jungle/internal/health"
	"jungle/internal/postgres"
	"jungle/internal/queue"
	"jungle/internal/router"
	"jungle/internal/server"
)

func main() {
	fx.New(
		fx.Provide(zap.NewProduction),
		fx.WithLogger(func(logger *zap.Logger) fxevent.Logger {
			return &fxevent.ZapLogger{Logger: logger}
		}),

		config.Module,
		server.Module,
		postgres.Module,
		queue.Module,

		health.Module,

		router.Module,
	).Run()
}
