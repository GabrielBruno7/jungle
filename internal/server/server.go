package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/fx"
	"go.uber.org/zap"

	"jungle/internal/config"
)

// NewEngine builds the gin engine used to serve every route in the app.
// gin.New() (unlike gin.Default()) attaches no middleware, so request
// logging and panic recovery are wired up explicitly here using zap, to
// stay consistent with the structured logging used everywhere else.
func NewEngine(logger *zap.Logger) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	engine := gin.New()
	engine.Use(recoveryMiddleware(logger), loggingMiddleware(logger))
	return engine
}

func loggingMiddleware(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		logger.Info("http request",
			zap.String("method", c.Request.Method),
			zap.String("path", path),
			zap.Int("status", c.Writer.Status()),
			zap.String("client_ip", c.ClientIP()),
			zap.Duration("latency", time.Since(start)),
		)
	}
}

func recoveryMiddleware(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				logger.Error("panic recovered",
					zap.Any("error", err),
					zap.String("method", c.Request.Method),
					zap.String("path", c.Request.URL.Path),
				)
				c.AbortWithStatus(http.StatusInternalServerError)
			}
		}()
		c.Next()
	}
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
