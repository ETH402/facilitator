//go:build integration

package migrate

import (
	"context"
	"os"
	"testing"

	"github.com/ETH402/facilitator/migrations"
	"github.com/jackc/pgx/v5"
)

func TestCheckAppliedRejectsMissingAndUnknownVersions(t *testing.T) {
	databaseURL := os.Getenv("ETH402_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ETH402_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	connection, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close(ctx) }()
	if err := Up(ctx, connection, migrations.Files); err != nil {
		t.Fatal(err)
	}
	if err := CheckApplied(ctx, connection, migrations.Files); err != nil {
		t.Fatalf("matching migrations rejected: %v", err)
	}
	if _, err := connection.Exec(ctx,
		`INSERT INTO schema_migrations(version) VALUES ('999999_unknown')`); err != nil {
		t.Fatal(err)
	}
	if err := CheckApplied(ctx, connection, migrations.Files); err == nil {
		t.Fatal("unknown database migration accepted")
	}
	if _, err := connection.Exec(ctx,
		`DELETE FROM schema_migrations WHERE version='999999_unknown'`); err != nil {
		t.Fatal(err)
	}
}
