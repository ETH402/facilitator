package stats

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Component states, ordered by severity. A component is never StateUnknown: a
// dependency that cannot be observed is an outage from a caller's point of view.
// StateUnknown applies only to the overall status, and only when there is nothing
// to reduce.
const (
	StateOperational = "operational"
	StateDegraded    = "degraded"
	StateOutage      = "outage"
	// StateDisabled marks a capability the operator has turned off. It is
	// deliberately not an outage — reporting a deliberate configuration as broken
	// trains people to ignore the status page.
	StateDisabled = "disabled"
	// StateUnknown is what a status page says when it cannot observe anything. It
	// exists because the alternative is defaulting to "operational", which is how
	// the previous constant-status field came to report health during outages: an
	// unobservable service is not a healthy one.
	StateUnknown = "unknown"
)

// Component is one dependency's observed state.
type Component struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// Pinger is the database. *store.Database satisfies it.
type Pinger interface {
	Ping(ctx context.Context) error
}

// ChainReader is the Ethereum RPC. *ethereum.Client satisfies it.
type ChainReader interface {
	ChainID(ctx context.Context) (uint64, error)
}

// Heartbeats reports when each settlement worker last completed a tick.
// *metrics.Registry satisfies it.
type Heartbeats interface {
	WorkerHeartbeats() map[string]time.Time
}

// Assessor observes the facilitator's dependencies and reports what it found.
//
// It exists because the previous status field was the constant "operational". A
// status page whose status cannot change is worse than no status page: it reports
// health during an outage, which is the one moment anybody reads it.
//
// Every check here is an observation with a timestamp behind it, never an
// assumption. In particular worker health is derived from heartbeats rather than
// from the workers having been started, because a started worker that stopped
// ticking is exactly the failure this is for.
type Assessor struct {
	database          Pinger
	chain             ChainReader
	expectedChainID   uint64
	heartbeats        Heartbeats
	expectedWorkers   []string
	staleAfter        time.Duration
	settlementEnabled bool
	probeTimeout      time.Duration
	now               func() time.Time
}

// AssessorConfig groups what an assessor needs. settlementEnabled must reflect
// whether a signer is configured: with settlement off, absent worker heartbeats
// are correct rather than a fault, and reporting an outage would be wrong.
type AssessorConfig struct {
	Database          Pinger
	Chain             ChainReader
	ExpectedChainID   uint64
	Heartbeats        Heartbeats
	ExpectedWorkers   []string
	StaleAfter        time.Duration
	SettlementEnabled bool
	ProbeTimeout      time.Duration
}

func NewAssessor(cfg AssessorConfig) *Assessor {
	probe := cfg.ProbeTimeout
	if probe <= 0 {
		probe = 3 * time.Second
	}
	stale := cfg.StaleAfter
	if stale <= 0 {
		stale = time.Minute
	}
	return &Assessor{
		database: cfg.Database, chain: cfg.Chain, expectedChainID: cfg.ExpectedChainID,
		heartbeats: cfg.Heartbeats, expectedWorkers: cfg.ExpectedWorkers,
		staleAfter: stale, settlementEnabled: cfg.SettlementEnabled,
		probeTimeout: probe, now: time.Now,
	}
}

// Components probes every dependency and returns their states, sorted so the
// output is stable for consumers and for tests.
func (a *Assessor) Components(ctx context.Context) []Component {
	if a == nil {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, a.probeTimeout)
	defer cancel()

	components := []Component{a.databaseComponent(probeCtx), a.chainComponent(probeCtx)}
	components = append(components, a.settlementComponent())
	sort.Slice(components, func(i, j int) bool { return components[i].Name < components[j].Name })
	return components
}

func (a *Assessor) databaseComponent(ctx context.Context) Component {
	if a.database == nil {
		return Component{Name: "database", State: StateOutage, Detail: "not configured"}
	}
	if err := a.database.Ping(ctx); err != nil {
		// The error itself is not published: it can carry hostnames, usernames, and
		// driver internals, and this endpoint is public.
		return Component{Name: "database", State: StateOutage, Detail: "unreachable"}
	}
	return Component{Name: "database", State: StateOperational}
}

func (a *Assessor) chainComponent(ctx context.Context) Component {
	if a.chain == nil {
		return Component{Name: "ethereum_rpc", State: StateOutage, Detail: "not configured"}
	}
	chainID, err := a.chain.ChainID(ctx)
	if err != nil {
		return Component{Name: "ethereum_rpc", State: StateOutage, Detail: "unreachable"}
	}
	if chainID != a.expectedChainID {
		// A reachable RPC on the wrong chain is worse than an unreachable one,
		// because everything downstream would appear to work.
		return Component{Name: "ethereum_rpc", State: StateOutage,
			Detail: fmt.Sprintf("reports chain %d, expected %d", chainID, a.expectedChainID)}
	}
	return Component{Name: "ethereum_rpc", State: StateOperational}
}

// settlementComponent reports the settlement pipeline as a whole rather than each
// worker, because to a caller the meaningful question is whether payments settle.
// A stalled worker is degraded and not an outage: verification keeps working and
// committed intents are durable, so payments are delayed rather than lost.
func (a *Assessor) settlementComponent() Component {
	if !a.settlementEnabled {
		return Component{Name: "settlement", State: StateDisabled, Detail: "no signer configured"}
	}
	if a.heartbeats == nil {
		return Component{Name: "settlement", State: StateDegraded, Detail: "worker health is not observable"}
	}
	seen := a.heartbeats.WorkerHeartbeats()
	now := a.now()
	var stale, missing []string
	for _, worker := range a.expectedWorkers {
		at, ok := seen[worker]
		if !ok {
			// Never ticked. On a fresh start this is briefly true and honest:
			// settlement genuinely has not run yet.
			missing = append(missing, worker)
			continue
		}
		if now.Sub(at) > a.staleAfter {
			stale = append(stale, worker)
		}
	}
	switch {
	case len(missing) > 0 && len(stale) > 0:
		return Component{Name: "settlement", State: StateDegraded,
			Detail: fmt.Sprintf("%d workers stalled, %d never started", len(stale), len(missing))}
	case len(stale) > 0:
		return Component{Name: "settlement", State: StateDegraded,
			Detail: fmt.Sprintf("%d workers stalled", len(stale))}
	case len(missing) > 0:
		return Component{Name: "settlement", State: StateDegraded,
			Detail: fmt.Sprintf("%d workers have not reported yet", len(missing))}
	}
	return Component{Name: "settlement", State: StateOperational}
}

// overall reduces components to one word, taking the worst state that matters.
//
// A disabled capability does not degrade the whole: an operator running
// verification only is operating as configured, and saying otherwise would make
// the field useless for the operators who do settle.
func overall(components []Component) string {
	if len(components) == 0 {
		return StateUnknown
	}
	status := StateOperational
	for _, component := range components {
		switch component.State {
		case StateOutage:
			return StateOutage
		case StateDegraded:
			status = StateDegraded
		}
	}
	return status
}
