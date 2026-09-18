package main

import (
	"testing"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
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

func modules() fx.Option {
	return fx.Options(
		fx.Provide(zap.NewNop),

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
	)
}

func TestFxGraphResolves(t *testing.T) {
	if err := fx.ValidateApp(modules()); err != nil {
		t.Fatalf("fx graph does not resolve: %v", err)
	}
}

func TestFxAppConstructs(t *testing.T) {
	appUnderTest := fxtest.New(t, modules(), fx.NopLogger)
	if err := appUnderTest.Err(); err != nil {
		t.Fatalf("constructing app: %v", err)
	}
}
