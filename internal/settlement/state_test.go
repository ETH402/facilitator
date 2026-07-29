package settlement

import "testing"

func TestTransitions(t *testing.T) {
	t.Parallel()
	if !CanTransition(StateReceived, StateVerified) {
		t.Fatal("expected received -> verified")
	}
	if CanTransition(StateConfirmed, StateBroadcasting) {
		t.Fatal("terminal state reopened")
	}
	if CanTransition(StateReceived, StateConfirmed) {
		t.Fatal("unsafe transition accepted")
	}
}

// Expired is terminal except for one escape: a nonce-gap filler the chain
// accepted means USDC moved on an authorization believed expired, so the record
// disagrees with the ledger and a human must reconcile.
func TestExpiredEscalatesOnlyToManualReview(t *testing.T) {
	if !CanTransition(StateExpired, StateManualReview) {
		t.Fatal("expired cannot escalate to manual review")
	}
	for _, to := range []State{
		StateReceived, StateVerified, StateBroadcasting, StateBroadcast,
		StateConfirming, StateConfirmed, StateFailed, StateReverted,
		StateReplaced, StateExpired, StateVerificationFailed,
	} {
		if CanTransition(StateExpired, to) {
			t.Fatalf("expired must not transition to %q; recovery never finalizes a payment", to)
		}
	}
}
