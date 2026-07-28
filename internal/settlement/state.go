package settlement

import "fmt"

type State string

const (
	StateReceived           State = "received"
	StateVerificationFailed State = "verification_failed"
	StateVerified           State = "verified"
	StateBroadcasting       State = "broadcasting"
	StateBroadcast          State = "broadcast"
	StateConfirming         State = "confirming"
	StateConfirmed          State = "confirmed"
	StateFailed             State = "failed"
	StateReverted           State = "reverted"
	StateReplaced           State = "replaced"
	StateExpired            State = "expired"
	StateManualReview       State = "manual_review"
)

var transitions = map[State]map[State]bool{
	StateReceived:     {StateVerificationFailed: true, StateVerified: true, StateExpired: true, StateManualReview: true},
	StateVerified:     {StateBroadcasting: true, StateExpired: true, StateManualReview: true},
	StateBroadcasting: {StateBroadcast: true, StateFailed: true, StateExpired: true, StateManualReview: true},
	StateBroadcast:    {StateConfirming: true, StateConfirmed: true, StateReverted: true, StateReplaced: true, StateManualReview: true},
	StateConfirming:   {StateBroadcast: true, StateConfirmed: true, StateReverted: true, StateReplaced: true, StateManualReview: true},
	StateReplaced:     {StateConfirming: true, StateConfirmed: true, StateReverted: true, StateManualReview: true},
	StateManualReview: {StateVerified: true, StateBroadcast: true, StateConfirming: true, StateFailed: true, StateExpired: true},
}

func CanTransition(from, to State) bool { return transitions[from][to] }

func ValidateTransition(from, to State) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("invalid payment transition %q -> %q", from, to)
	}
	return nil
}
