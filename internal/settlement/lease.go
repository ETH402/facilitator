package settlement

import (
	"errors"
	"time"
)

// ErrLeaseLost means the worker no longer holds the lease it is acting on,
// because the lease expired and another worker claimed the payment. A worker
// that sees this must stop touching the payment: its own work may already be
// duplicated, and continuing risks two workers broadcasting the same intent.
var ErrLeaseLost = errors.New("worker no longer holds the payment lease")

// Lease is a time-bounded claim on a payment. It exists so a worker can release
// its database transaction before performing Ethereum RPC, which ADR-0004
// decision 10 requires: holding a transaction open across network calls lets a
// slow provider exhaust the connection pool and take HTTP serving down with it.
type Lease struct {
	PaymentID       string
	PaymentIdentity string
	State           State
	Worker          string
	Until           time.Time
}

// Expired reports whether the lease has lapsed and may be claimed by others.
func (l Lease) Expired(now time.Time) bool { return !l.Until.After(now) }

// ClaimRequest asks for up to Limit payments in any of States.
type ClaimRequest struct {
	// Worker identifies the holder. It appears in the durable row, so it should
	// name the process well enough to trace a stuck lease back to it.
	Worker   string
	States   []State
	Duration time.Duration
	Limit    int
	Now      time.Time
}

func (r ClaimRequest) Validate() error {
	var errs []error
	if r.Worker == "" {
		errs = append(errs, errors.New("worker identity is required"))
	}
	if len(r.States) == 0 {
		errs = append(errs, errors.New("at least one claimable state is required"))
	}
	if r.Duration <= 0 {
		// A non-positive lease would be expired on arrival, so every worker could
		// claim the same payment at once.
		errs = append(errs, errors.New("lease duration must be positive"))
	}
	if r.Limit <= 0 {
		errs = append(errs, errors.New("claim limit must be positive"))
	}
	if r.Now.IsZero() {
		errs = append(errs, errors.New("current time is required"))
	}
	return errors.Join(errs...)
}

// StateStrings renders the claimable states for a SQL parameter.
func (r ClaimRequest) StateStrings() []string {
	states := make([]string, 0, len(r.States))
	for _, state := range r.States {
		states = append(states, string(state))
	}
	return states
}
