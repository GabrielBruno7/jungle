package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"
	"go.uber.org/zap"

	"jungle/internal/config"
)

func NewEngine(logger *zap.Logger, cfg *config.Config, tracerProvider trace.TracerProvider) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	engine := gin.New()
	engine.Use(
		otelgin.Middleware(cfg.Tracing.ServiceName,
			otelgin.WithTracerProvider(tracerProvider),
			otelgin.WithFilter(func(r *http.Request) bool {
				switch r.URL.Path {
				case "/health/live", "/health/ready", "/metrics":
					return false
				default:
					return true
				}
			}),
		),
		recoveryMiddleware(logger),
		loggingMiddleware(logger),
	)
	return engine
}

func loggingMiddleware(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		fields := []zap.Field{
			zap.String("method", c.Request.Method),
			zap.String("path", path),
			zap.Int("status", c.Writer.Status()),
			zap.String("client_ip", c.ClientIP()),
			zap.Duration("latency", time.Since(start)),
		}
		if correlationID := c.Writer.Header().Get("X-Correlation-Id"); correlationID != "" {
			fields = append(fields, zap.String("correlationId", correlationID))
		} else if correlationID := c.GetHeader("X-Correlation-Id"); correlationID != "" {
			fields = append(fields, zap.String("correlationId", correlationID))
		}

		if len(c.Errors) > 0 {
			logger.Error("http request failed",
				append(fields, zap.String("error", c.Errors.String()))...)
			return
		}

		logger.Info("http request", fields...)
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

var Module = fx.Module("server",
	fx.Provide(
		NewEngine,
		NewHTTPServer,
	),
	fx.Invoke(func(*http.Server) {}),
)
