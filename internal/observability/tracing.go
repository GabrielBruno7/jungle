package observability

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/fx"
	"go.uber.org/zap"

	"jungle/internal/config"
)

const TracerName = "jungle"

func Tracer() trace.Tracer {
	return otel.Tracer(TracerName)
}

func NewTracerProvider(lc fx.Lifecycle, cfg *config.Config, logger *zap.Logger) (trace.TracerProvider, error) {
	if !cfg.Tracing.Enabled {
		provider := noop.NewTracerProvider()
		otel.SetTracerProvider(provider)
		otel.SetTextMapPropagator(propagator())
		logger.Info("tracing disabled")
		return provider, nil
	}

	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName(cfg.Tracing.ServiceName),
		semconv.ServiceInstanceID(cfg.InstanceID),
	))
	if err != nil {
		return nil, fmt.Errorf("building tracing resource: %w", err)
	}

	exporter, err := otlptracegrpc.New(context.Background(),
		otlptracegrpc.WithEndpoint(cfg.Tracing.Endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("creating otlp exporter: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.Tracing.SampleRatio))),
	)

	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagator())

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			logger.Info("tracing enabled",
				zap.String("endpoint", cfg.Tracing.Endpoint),
				zap.String("service", cfg.Tracing.ServiceName),
				zap.Float64("sampleRatio", cfg.Tracing.SampleRatio),
			)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()

			if err := provider.Shutdown(flushCtx); err != nil {
				logger.Warn("flushing pending spans failed", zap.Error(err))
			}
			return nil
		},
	})

	return provider, nil
}

func propagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}

func WalletAttributes(walletID, playerID string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("jungle.wallet_id", walletID),
		attribute.String("jungle.player_id", playerID),
	}
}

func OperationAttributes(providerID, externalID, kind string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("jungle.provider_id", providerID),
		attribute.String("jungle.external_transaction_id", externalID),
		attribute.String("jungle.kind", kind),
	}
}

func OutcomeAttributes(transactionID, status, failureCode string, replay bool) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("jungle.transaction_id", transactionID),
		attribute.String("jungle.status", status),
		attribute.Bool("jungle.idempotent_replay", replay),
	}
	if failureCode != "" {
		attrs = append(attrs, attribute.String("jungle.failure_code", failureCode))
	}
	return attrs
}

var TracingModule = fx.Module("tracing",
	fx.Provide(NewTracerProvider),
	fx.Invoke(func(trace.TracerProvider) {}),
)
