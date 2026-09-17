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

		// Feature modules: each one provides its own routes/services and
		// plugs into the graph without router or server knowing it exists.
		health.Module,

		// router.Module must come after every feature module so it sees
		// all routes registered in the "routes" group.
		router.Module,
	).Run()
}
