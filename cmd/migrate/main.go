package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"

	"github.com/ETH402/facilitator/internal/migrate"
	"github.com/ETH402/facilitator/migrations"
	"github.com/jackc/pgx/v5"
)

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "up" && os.Args[1] != "down") {
		fmt.Fprintln(os.Stderr, "usage: migrate up|down")
		os.Exit(2)
	}
	databaseURL, err := migrationDatabaseURL()
	if err != nil {
		slog.Error("invalid migration configuration", "error", err)
		os.Exit(1)
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		slog.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer func() {
		if closeErr := conn.Close(ctx); closeErr != nil {
			slog.Error("database close failed", "error", closeErr)
		}
	}()
	if os.Args[1] == "up" {
		err = migrate.Up(ctx, conn, migrations.Files)
	} else {
		err = migrate.Down(ctx, conn, migrations.Files)
	}
	if err != nil {
		slog.Error("migration failed", "direction", os.Args[1], "error", err)
		os.Exit(1)
	}
	slog.Info("migration complete", "direction", os.Args[1])
}

// migrationDatabaseURL deliberately does not load the application Config. The
// migration workload needs one credential and no RPC, SMTP, signer, operator,
// or encryption secrets. Keeping those values out of its environment preserves
// the migration/runtime identity boundary described in docs/POSTGRESQL_ROLES.md.
func migrationDatabaseURL() (string, error) {
	databaseURL := os.Getenv("ETH402_DATABASE_URL")
	if databaseURL == "" {
		return "", errors.New("ETH402_DATABASE_URL is required")
	}
	environment := os.Getenv("ETH402_ENV")
	if environment == "" {
		environment = "development"
	}
	if environment != "development" && environment != "test" && environment != "production" {
		return "", errors.New("ETH402_ENV must be development, test, or production")
	}
	if environment != "production" {
		return databaseURL, nil
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") ||
		parsed.Query().Get("sslmode") != "verify-full" {
		return "", errors.New("production migration database URL must use sslmode=verify-full")
	}
	return databaseURL, nil
}
