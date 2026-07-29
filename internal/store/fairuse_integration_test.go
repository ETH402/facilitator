//go:build integration

package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestFairUseCountsExactlyUnderConcurrency is the whole reason accounting is one
// statement rather than a read followed by a write. With a read-then-write, two
// instances both observe used == limit-1 and both allow, so the limit is not a
// limit — and that is invisible in a sequential test.
func TestFairUseCountsExactlyUnderConcurrency(t *testing.T) {
	store := settlementTestStore(t)
	pool := store.Pool
	merchantID := seedFairUseMerchant(t, store)
	const (
		limit    = 50
		requests = 400
		workers  = 16
	)
	window := time.Hour
	now := time.Now()

	var mu sync.Mutex
	allowed, refused := 0, 0
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range requests / workers {
				usage, err := CountMerchantRequest(context.Background(), pool, merchantID, limit, window, now)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				if usage.Exceeded {
					refused++
				} else {
					allowed++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if allowed != limit {
		t.Errorf("allowed %d requests against a limit of %d (refused %d); "+
			"concurrent accounting must be exact", allowed, limit, refused)
	}
	if allowed+refused != requests {
		t.Errorf("accounted %d of %d requests", allowed+refused, requests)
	}
}

// TestFairUseWindowsAreIndependent checks the tumbling boundary. A merchant that
// exhausted one window must be served in the next, or the control is a permanent
// ban rather than a rate limit.
func TestFairUseWindowsAreIndependent(t *testing.T) {
	store := settlementTestStore(t)
	pool := store.Pool
	merchantID := seedFairUseMerchant(t, store)
	window := time.Hour
	first := time.Now().Truncate(window)

	for range 3 {
		if _, err := CountMerchantRequest(context.Background(), pool, merchantID, 2, window, first); err != nil {
			t.Fatal(err)
		}
	}
	exhausted, err := CountMerchantRequest(context.Background(), pool, merchantID, 2, window, first)
	if err != nil {
		t.Fatal(err)
	}
	if !exhausted.Exceeded {
		t.Fatal("a merchant past its limit was not refused")
	}

	next, err := CountMerchantRequest(context.Background(), pool, merchantID, 2, window, first.Add(window))
	if err != nil {
		t.Fatal(err)
	}
	if next.Exceeded {
		t.Error("the next window must start fresh; a fair-use limit is not a ban")
	}
	if next.Used != 1 {
		t.Errorf("new window started at %d, want 1", next.Used)
	}
}

// TestFairUseReportsResetAndRemaining covers the headers a client needs to back
// off correctly rather than retry blindly.
func TestFairUseReportsResetAndRemaining(t *testing.T) {
	store := settlementTestStore(t)
	pool := store.Pool
	merchantID := seedFairUseMerchant(t, store)
	window := 15 * time.Minute
	now := time.Now()

	usage, err := CountMerchantRequest(context.Background(), pool, merchantID, 10, window, now)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Remaining != 9 {
		t.Errorf("remaining = %d, want 9", usage.Remaining)
	}
	if want := now.Truncate(window).Add(window); !usage.ResetsAt.Equal(want) {
		t.Errorf("resets at %s, want %s", usage.ResetsAt, want)
	}
	// Remaining must floor at zero: a negative allowance in a header is nonsense
	// and clients do arithmetic on it.
	for range 20 {
		if usage, err = CountMerchantRequest(context.Background(), pool, merchantID, 10, window, now); err != nil {
			t.Fatal(err)
		}
	}
	if usage.Remaining != 0 {
		t.Errorf("remaining = %d after exceeding the limit, want 0", usage.Remaining)
	}
}

// TestPruneMerchantUsageKeepsTheCurrentWindow guards against pruning deleting rows
// that are still being written, which would silently reset live allowances.
func TestPruneMerchantUsageKeepsTheCurrentWindow(t *testing.T) {
	store := settlementTestStore(t)
	pool := store.Pool
	merchantID := seedFairUseMerchant(t, store)
	window := time.Hour
	now := time.Now()

	for _, at := range []time.Time{
		now,                    // current
		now.Add(-window),       // previous, retained as skew headroom
		now.Add(-5 * window),   // stale
		now.Add(-100 * window), // very stale
	} {
		if _, err := CountMerchantRequest(context.Background(), pool, merchantID, 1000, window, at); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := PruneMerchantUsage(context.Background(), pool, window, now)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Errorf("pruned %d rows, want 2 (the two stale windows)", removed)
	}
	current, err := CountMerchantRequest(context.Background(), pool, merchantID, 1000, window, now)
	if err != nil {
		t.Fatal(err)
	}
	if current.Used != 2 {
		t.Errorf("current window shows %d requests, want 2 — pruning must not delete a live window", current.Used)
	}
}

func seedFairUseMerchant(t *testing.T, store *Store) string {
	t.Helper()
	var id string
	err := store.Pool.QueryRow(context.Background(), `
INSERT INTO merchants
(name,business_email,email_domain,recipient_address,terms_version,terms_accepted_at,status,email_verified_at,wallet_verified_at)
VALUES ('Fair Use','fairuse@example.com','example.com','0x2222222222222222222222222222222222222222','v1',now(),'active',now(),now())
RETURNING id`).Scan(&id)
	if err != nil {
		t.Fatalf("seed merchant: %v", err)
	}
	return id
}
