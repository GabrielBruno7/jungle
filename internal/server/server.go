package server

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/fx"
	"go.uber.org/zap"

	"jungle/internal/config"
)

// NewEngine builds the gin engine used to serve every route in the app.
func NewEngine() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	return gin.New()
}

// NewHTTPServer wraps the gin engine in an http.Server whose lifecycle is
// tied to the fx.App: it starts listening on OnStart and shuts down
// gracefully on OnStop.
func NewHTTPServer(lc fx.Lifecycle, engine *gin.Engine, cfg *config.Config, logger *zap.Logger) *http.Server {
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: engine,
	}

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ln, err := net.Listen("tcp", srv.Addr)
			if err != nil {
				return err
			}
			logger.Info("starting HTTP server", zap.String("addr", srv.Addr))
			go srv.Serve(ln)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.Info("stopping HTTP server")
			return srv.Shutdown(ctx)
		},
	})

	return srv
}

// Module provides the gin engine and the http.Server that serves it, and
// forces the server to be built (and thus started) even though nothing
// else in the graph depends on it directly.
var Module = fx.Module("server",
	fx.Provide(
		NewEngine,
		NewHTTPServer,
	),
	fx.Invoke(func(*http.Server) {}),
)
