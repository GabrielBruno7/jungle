package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/fx"
)

type Config struct {
	Port int

	InstanceID string

	Postgres Postgres
	SQS      SQS
	OIDC     OIDC
	Worker   Worker
	Startup  Startup
	Tracing  Tracing
}

type Tracing struct {
	Enabled     bool
	Endpoint    string
	ServiceName string
	SampleRatio float64
}

type Startup struct {
	DependencyTimeout time.Duration
}

type OIDC struct {
	IssuerURL string

	AdditionalIssuers []string

	Audience         string
	DiscoveryTimeout time.Duration
}

type Worker struct {
	OutboxInterval    time.Duration
	ReferenceInterval time.Duration
	ShutdownTimeout   time.Duration
}

type Postgres struct {
	Host     string
	Port     int
	User     string
	Password string
	DBName   string
	SSLMode  string
}

type SQS struct {
	Region string

	Endpoint string

	RequestQueueURL string

	EventQueueURL string

	ConsumerName string

	MaxMessages       int32
	WaitTimeSeconds   int32
	VisibilityTimeout int32
}

func New() *Config {
	return &Config{
		Port:       envInt("JUNGLE_PORT", 8080),
		InstanceID: envString("JUNGLE_INSTANCE_ID", defaultInstanceID()),

		Postgres: Postgres{
			Host:     envString("POSTGRES_HOST", "localhost"),
			Port:     envInt("POSTGRES_PORT", 5432),
			User:     envString("POSTGRES_USER", "jungle"),
			Password: envString("POSTGRES_PASSWORD", "jungle"),
			DBName:   envString("POSTGRES_DB", "jungle"),
			SSLMode:  envString("POSTGRES_SSLMODE", "disable"),
		},

		SQS: SQS{
			Region:            envString("AWS_REGION", "us-east-1"),
			Endpoint:          envString("SQS_ENDPOINT_URL", ""),
			RequestQueueURL:   envString("SQS_REQUEST_QUEUE_URL", ""),
			EventQueueURL:     envString("SQS_EVENT_QUEUE_URL", ""),
			ConsumerName:      envString("SQS_CONSUMER_NAME", "wager-transactions-consumer"),
			MaxMessages:       int32(envInt("SQS_MAX_MESSAGES", 10)),
			WaitTimeSeconds:   int32(envInt("SQS_WAIT_TIME_SECONDS", 10)),
			VisibilityTimeout: int32(envInt("SQS_VISIBILITY_TIMEOUT", 60)),
		},

		OIDC: OIDC{
			IssuerURL:         envString("OIDC_ISSUER_URL", "http://localhost:8081/realms/jungle"),
			AdditionalIssuers: envList("OIDC_ADDITIONAL_ISSUERS"),
			Audience:          envString("OIDC_AUDIENCE", "jungle-api"),
			DiscoveryTimeout:  envDuration("OIDC_DISCOVERY_TIMEOUT", 90*time.Second),
		},

		Tracing: Tracing{
			Enabled:     envBool("TRACING_ENABLED", true),
			Endpoint:    envString("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
			ServiceName: envString("OTEL_SERVICE_NAME", "jungle"),
			SampleRatio: envFloat("TRACING_SAMPLE_RATIO", 1.0),
		},

		Startup: Startup{
			DependencyTimeout: envDuration("STARTUP_DEPENDENCY_TIMEOUT", 60*time.Second),
		},

		Worker: Worker{
			OutboxInterval:    envDuration("OUTBOX_INTERVAL", time.Second),
			ReferenceInterval: envDuration("REFERENCE_INTERVAL", 5*time.Second),
			ShutdownTimeout:   envDuration("SHUTDOWN_TIMEOUT", 20*time.Second),
		},
	}
}

func defaultInstanceID() string {
	if host, err := os.Hostname(); err == nil && host != "" {
		return fmt.Sprintf("%s-%d", host, os.Getpid())
	}
	return uuid.NewString()
}

func envList(key string) []string {
	raw := os.Getenv(key)
	if raw == "" {
		return nil
	}
	var out []string
	for _, item := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func envString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			return p
		}
	}
	return fallback
}

var Module = fx.Module("config",
	fx.Provide(New),
)

func envBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func envFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}
