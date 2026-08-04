//go:build integration

package store

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ETH402/facilitator/internal/migrate"
	"github.com/ETH402/facilitator/migrations"
	"github.com/jackc/pgx/v5"
)

func TestSettlementRecoveryDowngradeRefusesReplacementHistory(t *testing.T) {
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
	defer func() {
		if err := migrate.Up(ctx, conn, migrations.Files); err != nil {
			t.Errorf("restore latest schema: %v", err)
		}
	}()
	if _, err := conn.Exec(ctx, "TRUNCATE merchants CASCADE"); err != nil {
		t.Fatal(err)
	}

	var merchantID, paymentID string
	if err := conn.QueryRow(ctx, `INSERT INTO merchants
		(name,business_email,email_domain,recipient_address,terms_version,terms_accepted_at,status)
		VALUES ('recovery merchant','recovery@example.com','example.com',
		'0x1111111111111111111111111111111111111111','test',now(),'active') RETURNING id`).Scan(&merchantID); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `INSERT INTO payment_records
		(payment_identity,merchant_id,x402_version,scheme,network,asset,payer_address,recipient_address,
		 amount_atomic,authorization_nonce,valid_after,valid_before,payload_hash,verification_status,state)
		VALUES (repeat('a',68),$1,2,'exact','eip155:1','0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48',
		        '0x2222222222222222222222222222222222222222','0x1111111111111111111111111111111111111111',
		        42,repeat('b',66),now()-interval '1 minute',now()+interval '1 hour',repeat('f',64),
		        'verified','confirmed') RETURNING id`, merchantID).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO ethereum_transactions
		(payment_id,tx_hash,signer_address,transaction_nonce,status)
		VALUES ($1,repeat('c',66),'0x3333333333333333333333333333333333333333',7,'replaced'),
		       ($1,repeat('d',66),'0x3333333333333333333333333333333333333333',7,'confirmed')`, paymentID); err != nil {
		t.Fatal(err)
	}

	for range 9 { // 000013 down through 000005, leaving 000004 current.
		if err := migrate.Down(ctx, conn, migrations.Files); err != nil {
			t.Fatal(err)
		}
	}
	err = migrate.Down(ctx, conn, migrations.Files)
	if err == nil || !strings.Contains(err.Error(), "replacement transaction history cannot be represented") {
		t.Fatalf("000004 downgrade error = %v, want actionable refusal", err)
	}

	var versionStillApplied, feeColumnStillPresent bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM schema_migrations WHERE version='000004_settlement_recovery'
	)`).Scan(&versionStillApplied); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_name='ethereum_transactions' AND column_name='max_fee_per_gas'
	)`).Scan(&feeColumnStillPresent); err != nil {
		t.Fatal(err)
	}
	if !versionStillApplied || !feeColumnStillPresent {
		t.Fatalf("refused downgrade changed schema: version=%t fee_column=%t", versionStillApplied, feeColumnStillPresent)
	}
}
