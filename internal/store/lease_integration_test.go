//go:build integration

package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/settlement"
)

func claimRequest(worker string, limit int) settlement.ClaimRequest {
	return settlement.ClaimRequest{
		Worker: worker, States: []settlement.State{settlement.StateBroadcasting},
		Duration: time.Minute, Limit: limit, Now: time.Now(),
	}
}

// seedBroadcasting creates payments already in the broadcasting state, which is
// what a worker claims.
func seedBroadcasting(t *testing.T, store *Store, count int) []string {
	t.Helper()
	ids := make([]string, 0, count)
	for i := range count {
		identity := fmt.Sprintf("pay_%s%02d", repeat("c", 62), i)
		ids = append(ids, seedPayment(t, store, paymentFixture{
			identity: identity, state: "broadcasting", registered: true,
		}))
	}
	return ids
}

func TestClaimPaymentsLeasesAndExcludes(t *testing.T) {
	ctx := context.Background()
	store := settlementTestStore(t)
	seedBroadcasting(t, store, 3)

	first, err := store.ClaimPayments(ctx, claimRequest("worker-a", 2))
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 {
		t.Fatalf("claimed %d, want 2", len(first))
	}
	// A second worker must not see rows already leased.
	second, err := store.ClaimPayments(ctx, claimRequest("worker-b", 5))
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 {
		t.Fatalf("second worker claimed %d, want the 1 remaining", len(second))
	}
	third, err := store.ClaimPayments(ctx, claimRequest("worker-c", 5))
	if err != nil {
		t.Fatal(err)
	}
	if len(third) != 0 {
		t.Fatalf("third worker claimed %d, want 0", len(third))
	}
	for _, lease := range first {
		if lease.Worker != "worker-a" || lease.State != settlement.StateBroadcasting || lease.Until.IsZero() {
			t.Fatalf("unexpected lease: %+v", lease)
		}
	}
}

func TestClaimPaymentsIgnoresOtherStates(t *testing.T) {
	ctx := context.Background()
	store := settlementTestStore(t)
	seedPayment(t, store, paymentFixture{
		identity: "pay_" + repeat("d", 64), state: "verified", registered: true,
	})
	seedPayment(t, store, paymentFixture{
		identity: "pay_" + repeat("e", 64), state: "confirmed", registered: true,
	})
	claimed, err := store.ClaimPayments(ctx, claimRequest("worker-a", 10))
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimed %d payments outside the requested states", len(claimed))
	}
}

func TestExpiredLeaseIsReclaimable(t *testing.T) {
	ctx := context.Background()
	store := settlementTestStore(t)
	seedBroadcasting(t, store, 1)

	// A worker that dies mid-task leaves a lease behind; work must not be stranded.
	past := time.Now().Add(-2 * time.Minute)
	stale := settlement.ClaimRequest{
		Worker: "worker-dead", States: []settlement.State{settlement.StateBroadcasting},
		Duration: time.Minute, Limit: 1, Now: past,
	}
	dead, err := store.ClaimPayments(ctx, stale)
	if err != nil {
		t.Fatal(err)
	}
	if len(dead) != 1 {
		t.Fatalf("claimed %d, want 1", len(dead))
	}
	if !dead[0].Expired(time.Now()) {
		t.Fatal("lease seeded in the past is not reported as expired")
	}
	live, err := store.ClaimPayments(ctx, claimRequest("worker-live", 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].PaymentID != dead[0].PaymentID {
		t.Fatalf("expired lease was not reclaimed: %+v", live)
	}
	// The dead worker must now learn it lost the lease rather than continuing.
	if err := store.ReleaseLease(ctx, dead[0].PaymentID, "worker-dead"); !errors.Is(err, settlement.ErrLeaseLost) {
		t.Fatalf("release by superseded worker returned %v, want ErrLeaseLost", err)
	}
}

func TestRenewLeaseRequiresLiveOwnership(t *testing.T) {
	ctx := context.Background()
	store := settlementTestStore(t)
	seedBroadcasting(t, store, 1)
	claimed, err := store.ClaimPayments(ctx, claimRequest("worker-a", 1))
	if err != nil {
		t.Fatal(err)
	}
	lease := claimed[0]

	now := time.Now()
	until, err := store.RenewLease(ctx, lease.PaymentID, "worker-a", now, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !until.After(lease.Until) {
		t.Fatalf("renewed until %v, not after original %v", until, lease.Until)
	}
	// Another worker cannot renew someone else's lease.
	if _, err := store.RenewLease(ctx, lease.PaymentID, "worker-b", now, time.Minute); !errors.Is(err, settlement.ErrLeaseLost) {
		t.Fatalf("foreign renew returned %v, want ErrLeaseLost", err)
	}
	// An expired lease must be re-claimed, never renewed: another worker may
	// already own the payment, and renewing would give two workers a live claim.
	future := until.Add(time.Minute)
	if _, err := store.RenewLease(ctx, lease.PaymentID, "worker-a", future, time.Minute); !errors.Is(err, settlement.ErrLeaseLost) {
		t.Fatalf("renew of a lapsed lease returned %v, want ErrLeaseLost", err)
	}
}

func TestReleaseLeaseMakesPaymentClaimableAgain(t *testing.T) {
	ctx := context.Background()
	store := settlementTestStore(t)
	seedBroadcasting(t, store, 1)
	claimed, err := store.ClaimPayments(ctx, claimRequest("worker-a", 1))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseLease(ctx, claimed[0].PaymentID, "worker-a"); err != nil {
		t.Fatal(err)
	}
	var claimedBy, claimedUntil *string
	if err := store.Pool.QueryRow(ctx,
		`SELECT claimed_by, claimed_until::text FROM payment_records WHERE id=$1`, claimed[0].PaymentID).
		Scan(&claimedBy, &claimedUntil); err != nil {
		t.Fatal(err)
	}
	// The paired CHECK constraint requires both columns to clear together.
	if claimedBy != nil || claimedUntil != nil {
		t.Fatalf("lease not fully cleared: by=%v until=%v", claimedBy, claimedUntil)
	}
	again, err := store.ClaimPayments(ctx, claimRequest("worker-b", 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 {
		t.Fatalf("released payment was not reclaimable")
	}
	// Releasing twice must report the loss rather than silently succeeding.
	if err := store.ReleaseLease(ctx, claimed[0].PaymentID, "worker-a"); !errors.Is(err, settlement.ErrLeaseLost) {
		t.Fatalf("double release returned %v, want ErrLeaseLost", err)
	}
}

// TestConcurrentWorkersNeverShareAPayment is the core safety property: two
// workers holding one payment would mean two transactions broadcasting the same
// settlement.
//
// Contention is deliberately maximal — many workers competing for a single
// claimable row, repeated over many rounds. An earlier version of this test used
// forty payments and a generous limit, and a read-then-write claim passed it,
// because each worker's read-and-update completed before the next worker's read
// began. One row and a limit of one leaves nowhere for that to hide.
func TestConcurrentWorkersNeverShareAPayment(t *testing.T) {
	ctx := context.Background()
	store := settlementTestStore(t)
	const workers = 8
	const rounds = 30

	for round := range rounds {
		if _, err := store.Pool.Exec(ctx, "TRUNCATE merchants, payment_records CASCADE"); err != nil {
			t.Fatal(err)
		}
		seedPayment(t, store, paymentFixture{
			identity: fmt.Sprintf("pay_%s%02d", repeat("c", 62), round),
			state:    "broadcasting", registered: true,
		})

		start := make(chan struct{})
		var wait sync.WaitGroup
		claims := make([][]settlement.Lease, workers)
		failures := make([]error, workers)
		for i := range workers {
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				claims[i], failures[i] = store.ClaimPayments(ctx,
					claimRequest(fmt.Sprintf("worker-%d", i), 1))
			}()
		}
		close(start)
		wait.Wait()

		owner := make(map[string]string, 1)
		total := 0
		for i, err := range failures {
			if err != nil {
				t.Fatalf("round %d worker %d failed: %v", round, i, err)
			}
			for _, lease := range claims[i] {
				if previous, seen := owner[lease.PaymentID]; seen {
					t.Fatalf("round %d: payment %s leased to both %s and %s",
						round, lease.PaymentID, previous, lease.Worker)
				}
				owner[lease.PaymentID] = lease.Worker
				total++
			}
		}
		// Exactly one worker may win, and the row must not be lost entirely.
		if total != 1 {
			t.Fatalf("round %d: %d workers claimed the single payment, want 1", round, total)
		}
	}
}

func TestConcurrentWorkersPartitionManyPayments(t *testing.T) {
	ctx := context.Background()
	store := settlementTestStore(t)
	const payments = 40
	const workers = 8
	seedBroadcasting(t, store, payments)

	start := make(chan struct{})
	var wait sync.WaitGroup
	claims := make([][]settlement.Lease, workers)
	failures := make([]error, workers)
	for i := range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			claims[i], failures[i] = store.ClaimPayments(ctx,
				claimRequest(fmt.Sprintf("worker-%d", i), payments))
		}()
	}
	close(start)
	wait.Wait()

	owner := make(map[string]string, payments)
	total := 0
	for i, err := range failures {
		if err != nil {
			t.Fatalf("worker %d failed: %v", i, err)
		}
		for _, lease := range claims[i] {
			if previous, seen := owner[lease.PaymentID]; seen {
				t.Fatalf("payment %s leased to both %s and %s", lease.PaymentID, previous, lease.Worker)
			}
			owner[lease.PaymentID] = lease.Worker
			total++
		}
	}
	// No payment claimed twice and none stranded, however the workers interleaved.
	if total != payments || len(owner) != payments {
		t.Fatalf("claimed %d payments across %d distinct, want %d", total, len(owner), payments)
	}
}

func TestClaimRequestValidation(t *testing.T) {
	ctx := context.Background()
	store := settlementTestStore(t)
	base := claimRequest("worker-a", 1)
	cases := map[string]func(*settlement.ClaimRequest){
		"no worker":         func(r *settlement.ClaimRequest) { r.Worker = "" },
		"no states":         func(r *settlement.ClaimRequest) { r.States = nil },
		"zero duration":     func(r *settlement.ClaimRequest) { r.Duration = 0 },
		"negative limit":    func(r *settlement.ClaimRequest) { r.Limit = -1 },
		"missing clock":     func(r *settlement.ClaimRequest) { r.Now = time.Time{} },
		"negative duration": func(r *settlement.ClaimRequest) { r.Duration = -time.Minute },
	}
	for name, mutate := range cases {
		request := base
		mutate(&request)
		if _, err := store.ClaimPayments(ctx, request); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}
