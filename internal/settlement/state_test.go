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
