//go:build integration

package store

import (
	"context"
	"os"
	"testing"

	"github.com/ETH402/facilitator/internal/migrate"
	"github.com/ETH402/facilitator/migrations"
	"github.com/jackc/pgx/v5"
)

// TestFairUseMigrationRollsBack proves 000008 is reversible, since a migration that
// cannot be undone turns a bad deploy into a database restore.
func TestFairUseMigrationRollsBack(t *testing.T) {
	url := os.Getenv("ETH402_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("ETH402_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if err := migrate.Up(ctx, conn, migrations.Files); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Down(ctx, conn, migrations.Files); err != nil {
		t.Fatalf("rolling back 000008: %v", err)
	}
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('merchant_usage') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("merchant_usage survived its own down migration")
	}
	if err := migrate.Up(ctx, conn, migrations.Files); err != nil {
		t.Fatalf("re-applying: %v", err)
	}
}
