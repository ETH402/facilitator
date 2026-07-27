package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/ETH402/facilitator/internal/config"
	"github.com/ETH402/facilitator/internal/migrate"
	"github.com/ETH402/facilitator/migrations"
	"github.com/jackc/pgx/v5"
)

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "up" && os.Args[1] != "down") {
		fmt.Fprintln(os.Stderr, "usage: migrate up|down")
		os.Exit(2)
	}
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, cfg.DatabaseURL)
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
