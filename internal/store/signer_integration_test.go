//go:build integration

package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/ETH402/facilitator/internal/migrate"
	"github.com/ETH402/facilitator/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testSignerAddress = "0x00000000000000000000000000000000000000a1"

func signerTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("ETH402_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ETH402_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, conn, migrations.Files); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(ctx); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `DELETE FROM signer_accounts WHERE signer_address = $1`, testSignerAddress); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestAllocateNonceRequiresSeededSequence(t *testing.T) {
	pool := signerTestPool(t)
	// Failing closed matters: guessing zero would reuse nonces already spent on
	// chain, and every such transaction is silently dropped.
	if _, err := AllocateNonce(context.Background(), pool, testSignerAddress); !errors.Is(err, ErrSignerAccountUnknown) {
		t.Fatalf("unseeded allocation returned %v, want ErrSignerAccountUnknown", err)
	}
}

func TestSeedSignerAccountIsIdempotentAndNeverRewinds(t *testing.T) {
	ctx := context.Background()
	pool := signerTestPool(t)
	if got, err := SeedSignerAccount(ctx, pool, testSignerAddress, 42); err != nil || got != 42 {
		t.Fatalf("seed = %d, %v; want 42", got, err)
	}
	// Re-seeding at the same value must not disturb the sequence.
	if got, err := SeedSignerAccount(ctx, pool, testSignerAddress, 42); err != nil || got != 42 {
		t.Fatalf("re-seed = %d, %v; want 42", got, err)
	}
	if _, err := AllocateNonce(ctx, pool, testSignerAddress); err != nil {
		t.Fatal(err)
	}
	// A provider reporting a stale transaction count must not rewind the stored
	// sequence onto a nonce that has already been handed out.
	if got, err := SeedSignerAccount(ctx, pool, testSignerAddress, 42); err != nil || got != 43 {
		t.Fatalf("stale re-seed = %d, %v; want 43", got, err)
	}
	// A genuinely advanced chain count moves the sequence forward.
	if got, err := SeedSignerAccount(ctx, pool, testSignerAddress, 100); err != nil || got != 100 {
		t.Fatalf("advanced re-seed = %d, %v; want 100", got, err)
	}
}

func TestAllocateNonceIsSequentialAndUniqueUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	pool := signerTestPool(t)
	if _, err := SeedSignerAccount(ctx, pool, testSignerAddress, 0); err != nil {
		t.Fatal(err)
	}

	const workers = 8
	const perWorker = 25
	const total = workers * perWorker

	var wait sync.WaitGroup
	results := make(chan uint64, total)
	failures := make(chan error, total)
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range perWorker {
				// Each allocation runs in its own transaction, exactly as a
				// settlement request would.
				tx, err := pool.Begin(ctx)
				if err != nil {
					failures <- err
					return
				}
				nonce, err := AllocateNonce(ctx, tx, testSignerAddress)
				if err != nil {
					_ = tx.Rollback(ctx)
					failures <- err
					return
				}
				if err := tx.Commit(ctx); err != nil {
					failures <- err
					return
				}
				results <- nonce
			}
		}()
	}
	wait.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatalf("concurrent allocation failed: %v", err)
	}

	seen := make(map[uint64]bool, total)
	for nonce := range results {
		if seen[nonce] {
			t.Fatalf("nonce %d was allocated twice; a duplicate nonce means one transaction is silently dropped", nonce)
		}
		seen[nonce] = true
	}
	if len(seen) != total {
		t.Fatalf("allocated %d distinct nonces, want %d", len(seen), total)
	}
	// The sequence must be gapless: a gap stalls every later transaction until it
	// is filled.
	for expected := range uint64(total) {
		if !seen[expected] {
			t.Fatalf("nonce %d is missing from the sequence", expected)
		}
	}
}

func TestAllocateNonceRolledBackWithItsTransaction(t *testing.T) {
	ctx := context.Background()
	pool := signerTestPool(t)
	if _, err := SeedSignerAccount(ctx, pool, testSignerAddress, 5); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := AllocateNonce(ctx, tx, testSignerAddress)
	if err != nil {
		t.Fatal(err)
	}
	if nonce != 5 {
		t.Fatalf("nonce = %d, want 5", nonce)
	}
	// A settlement that fails before commit must not burn the nonce, or the
	// sequence gaps and every later transaction stalls behind it.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := AllocateNonce(ctx, pool, testSignerAddress)
	if err != nil {
		t.Fatal(err)
	}
	if again != 5 {
		t.Fatalf("nonce after rollback = %d, want 5 reissued", again)
	}
}

func TestSignerAddressIsNormalized(t *testing.T) {
	ctx := context.Background()
	pool := signerTestPool(t)
	if _, err := SeedSignerAccount(ctx, pool, "0x00000000000000000000000000000000000000A1", 3); err != nil {
		t.Fatal(err)
	}
	// The mixed-case and lowercase forms must be the same sequence, not two.
	if got, err := AllocateNonce(ctx, pool, testSignerAddress); err != nil || got != 3 {
		t.Fatalf("allocation = %d, %v; want 3", got, err)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM signer_accounts WHERE signer_address = $1`, testSignerAddress).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("signer_accounts rows = %d, want 1", rows)
	}
	if _, err := SeedSignerAccount(ctx, pool, "not-an-address", 0); err == nil {
		t.Fatal("malformed signer address accepted")
	}
}
