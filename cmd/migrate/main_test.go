package main

import "testing"

func TestMigrationDatabaseURLRequiresOnlyDatabaseConfiguration(t *testing.T) {
	t.Setenv("ETH402_ENV", "development")
	t.Setenv("ETH402_DATABASE_URL", "postgres://migration@localhost/eth402?sslmode=disable")

	got, err := migrationDatabaseURL()
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgres://migration@localhost/eth402?sslmode=disable" {
		t.Fatalf("migrationDatabaseURL() = %q", got)
	}
}

func TestMigrationDatabaseURLRequiresDatabaseURL(t *testing.T) {
	t.Setenv("ETH402_ENV", "development")
	t.Setenv("ETH402_DATABASE_URL", "")
	if _, err := migrationDatabaseURL(); err == nil {
		t.Fatal("empty migration database URL accepted")
	}
}

func TestMigrationDatabaseURLRejectsUnknownEnvironment(t *testing.T) {
	t.Setenv("ETH402_ENV", "prod")
	t.Setenv("ETH402_DATABASE_URL", "postgres://migration@localhost/eth402?sslmode=disable")
	if _, err := migrationDatabaseURL(); err == nil {
		t.Fatal("unknown migration environment accepted")
	}
}

func TestProductionMigrationRequiresVerifiedTLS(t *testing.T) {
	t.Setenv("ETH402_ENV", "production")
	for _, databaseURL := range []string{
		"postgres://migration@db.internal/eth402?sslmode=disable",
		"postgres://migration@db.internal/eth402?sslmode=require",
		"mysql://migration@db.internal/eth402?sslmode=verify-full",
	} {
		t.Setenv("ETH402_DATABASE_URL", databaseURL)
		if _, err := migrationDatabaseURL(); err == nil {
			t.Fatalf("unsafe production database URL accepted: %s", databaseURL)
		}
	}

	const safe = "postgres://migration@db.internal/eth402?sslmode=verify-full"
	t.Setenv("ETH402_DATABASE_URL", safe)
	got, err := migrationDatabaseURL()
	if err != nil {
		t.Fatal(err)
	}
	if got != safe {
		t.Fatalf("migrationDatabaseURL() = %q", got)
	}
}
