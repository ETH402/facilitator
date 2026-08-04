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

// TestLatestMigrationsRollBack proves 000008 through 000013 are reversible
// before retention has redacted data. A migration that cannot be undone turns a
// bad deploy into a database restore; 000009 deliberately refuses rollback after
// redaction because restoring invented authorization values would be worse.
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
	if _, err := conn.Exec(ctx, "TRUNCATE merchants CASCADE"); err != nil {
		t.Fatal(err)
	}
	var merchantID, pendingTokenID string
	if err := conn.QueryRow(ctx, `INSERT INTO merchants
		(name,business_email,email_domain,recipient_address,terms_version,terms_accepted_at)
		VALUES ('migration merchant','migration@example.com','example.com',
		'0x1111111111111111111111111111111111111111','test',now()) RETURNING id`).Scan(&merchantID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO email_verification_tokens
		(merchant_id,token_hash,expires_at,sent_at)
		VALUES ($1,repeat('a',64),now()+interval '1 hour',now())`, merchantID); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `INSERT INTO email_verification_tokens
		(merchant_id,token_hash,expires_at)
		VALUES ($1,repeat('b',64),now()+interval '1 hour') RETURNING id`, merchantID).Scan(&pendingTokenID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO email_delivery_outbox
		(merchant_id,token_id,message_kind,token_ciphertext)
		VALUES ($1,$2,'registration',decode('01','hex'))`, merchantID, pendingTokenID); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Down(ctx, conn, migrations.Files); err != nil {
		t.Fatalf("rolling back 000013: %v", err)
	}
	var outboxExists, sentNullable bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('email_delivery_outbox') IS NOT NULL`).Scan(&outboxExists); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT is_nullable='YES' FROM information_schema.columns
		WHERE table_name='email_verification_tokens' AND column_name='sent_at'`).Scan(&sentNullable); err != nil {
		t.Fatal(err)
	}
	if outboxExists || sentNullable {
		t.Error("email outbox schema survived the 000013 down migration")
	}
	var deliveredTokens, pendingTokens int
	if err := conn.QueryRow(ctx, `SELECT count(*) FILTER (WHERE token_hash=repeat('a',64)),
		count(*) FILTER (WHERE token_hash=repeat('b',64)) FROM email_verification_tokens`).Scan(
		&deliveredTokens, &pendingTokens); err != nil {
		t.Fatal(err)
	}
	if deliveredTokens != 1 || pendingTokens != 0 {
		t.Fatalf("000013 rollback kept delivered/pending tokens = %d/%d, want 1/0", deliveredTokens, pendingTokens)
	}
	if err := migrate.Down(ctx, conn, migrations.Files); err != nil {
		t.Fatalf("rolling back 000012: %v", err)
	}
	var publicProfileColumnExists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_name='merchants' AND column_name='public_profile_opted_in_at'
	)`).Scan(&publicProfileColumnExists); err != nil {
		t.Fatal(err)
	}
	if publicProfileColumnExists {
		t.Error("public profile consent survived the 000012 down migration")
	}
	if err := migrate.Down(ctx, conn, migrations.Files); err != nil {
		t.Fatalf("rolling back 000011: %v", err)
	}
	var adminTableExists, consentColumnExists bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('merchant_admin_sessions') IS NOT NULL`).Scan(&adminTableExists); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_name='merchants' AND column_name='stats_opted_in_at'
	)`).Scan(&consentColumnExists); err != nil {
		t.Fatal(err)
	}
	if adminTableExists || consentColumnExists {
		t.Error("merchant admin schema survived the 000011 down migration")
	}
	if err := migrate.Down(ctx, conn, migrations.Files); err != nil {
		t.Fatalf("rolling back 000010: %v", err)
	}
	var backoffColumnExists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_name='ethereum_transactions' AND column_name='ambiguous_attempts'
	)`).Scan(&backoffColumnExists); err != nil {
		t.Fatal(err)
	}
	if backoffColumnExists {
		t.Error("ambiguous_attempts survived the 000010 down migration")
	}
	if err := migrate.Down(ctx, conn, migrations.Files); err != nil {
		t.Fatalf("rolling back 000009: %v", err)
	}
	var retentionColumnExists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_name='payment_records' AND column_name='redacted_at'
	)`).Scan(&retentionColumnExists); err != nil {
		t.Fatal(err)
	}
	if retentionColumnExists {
		t.Error("redacted_at survived the 000009 down migration")
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
