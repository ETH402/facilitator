//go:build integration

package store

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/migrate"
	"github.com/ETH402/facilitator/internal/verification"
	"github.com/ETH402/facilitator/migrations"
	"github.com/jackc/pgx/v5"
)

func TestMigrationsConstraintsAndStats(t *testing.T) {
	databaseURL := os.Getenv("ETH402_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ETH402_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if err := migrate.Up(ctx, conn, migrations.Files); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "TRUNCATE merchants CASCADE"); err != nil {
		t.Fatal(err)
	}
	var merchantID string
	err = conn.QueryRow(ctx, `INSERT INTO merchants
		(name,business_email,email_domain,recipient_address,terms_version,terms_accepted_at,status,email_verified_at,wallet_verified_at)
		VALUES ('Test','test@example.com','example.com','0x1111111111111111111111111111111111111111','v1',now(),'active',now(),now())
		RETURNING id`).Scan(&merchantID)
	if err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, databaseURL, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("PostgreSQL readiness failed: %v", err)
	}

	const insert = `INSERT INTO payment_records
		(payment_identity,merchant_id,x402_version,scheme,network,asset,payer_address,recipient_address,
		 amount_atomic,authorization_nonce,valid_after,valid_before,payload_hash,verification_status,state,
		 settlement_requested_at,confirmed_at)
		VALUES ($1,$2,2,'exact','eip155:1',$3,$4,$5,1000001,$6,now()-interval '1 minute',
		        now()+interval '1 minute',$7,'verified','confirmed',now(),now())`
	args := []any{
		"pay_" + repeat("a", 64), merchantID,
		"0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
		"0x2222222222222222222222222222222222222222",
		"0x1111111111111111111111111111111111111111",
		"0x" + repeat("b", 64), repeat("c", 64),
	}
	var successes atomic.Int32
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := store.Pool.Exec(ctx, insert, args...); err == nil {
				successes.Add(1)
			}
		}()
	}
	wait.Wait()
	if successes.Load() != 1 {
		t.Fatalf("duplicate payment inserts succeeded %d times", successes.Load())
	}
	if _, err := store.Pool.Exec(ctx, `INSERT INTO verification_attempts(payment_identity,result)
		VALUES ($1,'verified'),($1,'failed')`, args[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool.Exec(ctx, `INSERT INTO settlement_attempts(payment_identity,result)
		VALUES ($1,'accepted')`, args[0]); err != nil {
		t.Fatal(err)
	}
	got, err := store.AggregateStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.RegisteredMerchants != 1 || got.VerifiedMerchants != 1 ||
		got.TotalVerifications != 2 || got.ConfirmedSettlements != 1 ||
		got.TotalPaymentVolumeAtomic != "1000001" {
		t.Fatalf("unexpected database aggregate: %+v", got)
	}

	attempt := verification.Attempt{
		PaymentIdentity: "pay_" + repeat("d", 64),
		Result:          "verified",
		Payment: &verification.Payment{
			Identity:  "pay_" + repeat("d", 64),
			Asset:     "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
			Payer:     "0x3333333333333333333333333333333333333333",
			Recipient: "0x1111111111111111111111111111111111111111",
			Amount:    "42", Nonce: "0x" + repeat("e", 64),
			ValidAfter: time.Now().Add(-time.Minute), ValidBefore: time.Now().Add(time.Minute),
			PayloadHash: repeat("f", 64),
		},
	}
	var recordWait sync.WaitGroup
	var recordFailures atomic.Int32
	for range 2 {
		recordWait.Add(1)
		go func() {
			defer recordWait.Done()
			if err := store.RecordVerification(ctx, attempt); err != nil {
				recordFailures.Add(1)
			}
		}()
	}
	recordWait.Wait()
	if recordFailures.Load() != 0 {
		t.Fatalf("duplicate verification recording failed %d times", recordFailures.Load())
	}
	var payments, attempts, transitions int
	if err := store.Pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM payment_records WHERE payment_identity=$1),
  (SELECT count(*) FROM verification_attempts WHERE payment_identity=$1),
  (SELECT count(*) FROM payment_transitions t JOIN payment_records p ON p.id=t.payment_id WHERE p.payment_identity=$1)
`, attempt.PaymentIdentity).Scan(&payments, &attempts, &transitions); err != nil {
		t.Fatal(err)
	}
	if payments != 1 || attempts != 2 || transitions != 1 {
		t.Fatalf("payments=%d attempts=%d transitions=%d", payments, attempts, transitions)
	}
	conflict := attempt
	conflict.PaymentIdentity = "pay_" + repeat("1", 64)
	conflict.Payment = &verification.Payment{}
	*conflict.Payment = *attempt.Payment
	conflict.Payment.Identity = conflict.PaymentIdentity
	conflict.Payment.PayloadHash = repeat("2", 64)
	if err := store.RecordVerification(ctx, conflict); err == nil {
		t.Fatal("same on-chain nonce with a different payment identity was accepted")
	}
}

func repeat(value string, count int) string {
	result := ""
	for range count {
		result += value
	}
	return result
}
