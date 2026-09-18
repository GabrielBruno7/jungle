package main

import (
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
	"go.uber.org/zap"

	"jungle/internal/app"
	"jungle/internal/config"
	"jungle/internal/health"
	"jungle/internal/httpapi"
	"jungle/internal/observability"
	"jungle/internal/postgres"
	"jungle/internal/queue"
	"jungle/internal/router"
	"jungle/internal/server"
	"jungle/internal/worker"
)

func main() {
	fx.New(
		fx.Provide(zap.NewProduction),
		fx.WithLogger(func(logger *zap.Logger) fxevent.Logger {
			return &fxevent.ZapLogger{Logger: logger}
		}),

		config.Module,
		postgres.Module,
		queue.Module,
		server.Module,

		app.Module,
		fx.Provide(func(cfg *config.Config) app.InstanceID { return app.InstanceID(cfg.InstanceID) }),

		httpapi.Module,
		health.Module,
		observability.TracingModule,
		observability.Module,

		worker.Module,

		router.Module,
	).Run()
}
