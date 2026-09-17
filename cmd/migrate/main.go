// Command migrate applies or reverts the versioned SQL migrations in
// migrations/ against the Postgres database described by the same
// environment variables the main app uses (see internal/config).
//
// Usage:
//
//	go run ./cmd/migrate up             # apply every pending migration
//	go run ./cmd/migrate down           # revert every applied migration
//	go run ./cmd/migrate down 1         # revert the last migration only
//	go run ./cmd/migrate version        # print the current schema version
package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"

	"jungle/internal/config"
)

func main() {
	if len(os.Args) < 2 {
		usageAndExit()
	}

	cfg := config.New()
	dsn := fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=%s",
		cfg.Postgres.User, cfg.Postgres.Password,
		cfg.Postgres.Host, cfg.Postgres.Port,
		cfg.Postgres.DBName, cfg.Postgres.SSLMode,
	)

	migrationsPath := os.Getenv("MIGRATIONS_PATH")
	if migrationsPath == "" {
		migrationsPath = "migrations"
	}

	m, err := migrate.New("file://"+migrationsPath, dsn)
	if err != nil {
		fatal(fmt.Errorf("opening migrator: %w", err))
	}
	defer m.Close()

	switch os.Args[1] {
	case "up":
		err = m.Up()
	case "down":
		if len(os.Args) >= 3 {
			steps, parseErr := strconv.Atoi(os.Args[2])
			if parseErr != nil {
				fatal(fmt.Errorf("invalid step count %q: %w", os.Args[2], parseErr))
			}
			err = m.Steps(-steps)
		} else {
			err = m.Down()
		}
	case "version":
		version, dirty, verErr := m.Version()
		if verErr != nil {
			fatal(fmt.Errorf("reading version: %w", verErr))
		}
		fmt.Printf("version=%d dirty=%v\n", version, dirty)
		return
	default:
		usageAndExit()
	}

	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		fatal(fmt.Errorf("running %q: %w", os.Args[1], err))
	}
	fmt.Println("ok")
}

func usageAndExit() {
	fmt.Fprintln(os.Stderr, "usage: migrate <up|down [steps]|version>")
	os.Exit(1)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
