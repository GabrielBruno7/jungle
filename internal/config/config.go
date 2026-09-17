package config

import (
	"os"
	"strconv"

	"go.uber.org/fx"
)

// Config holds process-wide configuration, sourced from environment variables.
type Config struct {
	Port int

	Postgres Postgres
	SQS      SQS
}

// Postgres holds connection settings for the primary database.
type Postgres struct {
	Host     string
	Port     int
	User     string
	Password string
	DBName   string
	SSLMode  string
}

// SQS holds settings to reach an SQS queue, real or LocalStack.
type SQS struct {
	Region string
	// Endpoint overrides the default AWS endpoint. Set it to point at
	// LocalStack for local development; leave empty to hit real AWS.
	Endpoint string
	QueueURL string
}

// New builds Config from the environment, falling back to sane defaults
// that match docker-compose.yml.
func New() *Config {
	return &Config{
		Port: envInt("JUNGLE_PORT", 8080),

		Postgres: Postgres{
			Host:     envString("POSTGRES_HOST", "localhost"),
			Port:     envInt("POSTGRES_PORT", 5432),
			User:     envString("POSTGRES_USER", "jungle"),
			Password: envString("POSTGRES_PASSWORD", "jungle"),
			DBName:   envString("POSTGRES_DB", "jungle"),
			SSLMode:  envString("POSTGRES_SSLMODE", "disable"),
		},

		SQS: SQS{
			Region:   envString("AWS_REGION", "us-east-1"),
			Endpoint: envString("SQS_ENDPOINT_URL", ""),
			QueueURL: envString("SQS_QUEUE_URL", ""),
		},
	}
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

// Module exposes Config to the fx graph.
var Module = fx.Module("config",
	fx.Provide(New),
)
