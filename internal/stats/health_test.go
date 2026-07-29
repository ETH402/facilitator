package stats

import (
	"context"
	"errors"
	"testing"
	"time"
)

type stubPinger struct{ err error }

func (s stubPinger) Ping(context.Context) error { return s.err }

type stubChain struct {
	id  uint64
	err error
}

func (s stubChain) ChainID(context.Context) (uint64, error) { return s.id, s.err }

type stubHeartbeats map[string]time.Time

func (s stubHeartbeats) WorkerHeartbeats() map[string]time.Time { return s }

var settlementWorkers = []string{"broadcast", "confirmation", "recovery"}

func assessorAt(t *testing.T, now time.Time, cfg AssessorConfig) *Assessor {
	t.Helper()
	if cfg.ExpectedWorkers == nil {
		cfg.ExpectedWorkers = settlementWorkers
	}
	if cfg.StaleAfter == 0 {
		cfg.StaleAfter = time.Minute
	}
	a := NewAssessor(cfg)
	a.now = func() time.Time { return now }
	return a
}

func stateOf(components []Component, name string) Component {
	for _, component := range components {
		if component.Name == name {
			return component
		}
	}
	return Component{Name: name, State: "absent"}
}

// TestStatusReflectsRealObservations is the test that matters. The status field was
// previously the constant "operational", so it reported health during an outage —
// the one moment anybody reads a status page. Each case must move it.
func TestStatusReflectsRealObservations(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	fresh := stubHeartbeats{
		"broadcast": now.Add(-5 * time.Second), "confirmation": now.Add(-5 * time.Second),
		"recovery": now.Add(-5 * time.Second),
	}
	healthy := AssessorConfig{
		Database: stubPinger{}, Chain: stubChain{id: 1}, ExpectedChainID: 1,
		Heartbeats: fresh, SettlementEnabled: true,
	}

	cases := []struct {
		name      string
		mutate    func(*AssessorConfig)
		want      string
		component string
		wantState string
	}{
		{name: "everything healthy", mutate: func(*AssessorConfig) {},
			want: StateOperational, component: "settlement", wantState: StateOperational},
		{name: "database unreachable",
			mutate:    func(c *AssessorConfig) { c.Database = stubPinger{err: errors.New("dial tcp: refused")} },
			want:      StateOutage,
			component: "database", wantState: StateOutage},
		{name: "rpc unreachable",
			mutate:    func(c *AssessorConfig) { c.Chain = stubChain{err: errors.New("timeout")} },
			want:      StateOutage,
			component: "ethereum_rpc", wantState: StateOutage},
		{name: "rpc on the wrong chain",
			mutate:    func(c *AssessorConfig) { c.Chain = stubChain{id: 8453} },
			want:      StateOutage,
			component: "ethereum_rpc", wantState: StateOutage},
		{name: "one worker stalled",
			mutate: func(c *AssessorConfig) {
				c.Heartbeats = stubHeartbeats{
					"broadcast": now.Add(-10 * time.Minute), "confirmation": now.Add(-time.Second),
					"recovery": now.Add(-time.Second),
				}
			},
			want:      StateDegraded,
			component: "settlement", wantState: StateDegraded},
		{name: "workers never reported",
			mutate:    func(c *AssessorConfig) { c.Heartbeats = stubHeartbeats{} },
			want:      StateDegraded,
			component: "settlement", wantState: StateDegraded},
		{name: "worker health unobservable",
			mutate:    func(c *AssessorConfig) { c.Heartbeats = nil },
			want:      StateDegraded,
			component: "settlement", wantState: StateDegraded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := healthy
			tc.mutate(&cfg)
			components := assessorAt(t, now, cfg).Components(context.Background())
			if got := overall(components); got != tc.want {
				t.Errorf("overall = %s, want %s (components %+v)", got, tc.want, components)
			}
			if got := stateOf(components, tc.component); got.State != tc.wantState {
				t.Errorf("%s = %s, want %s", tc.component, got.State, tc.wantState)
			}
		})
	}
}

// TestDisabledSettlementIsNotAnOutage guards against the status page crying wolf.
// An operator running verification only is operating as configured; reporting that
// as broken trains people to ignore the page, which costs more than it saves.
func TestDisabledSettlementIsNotAnOutage(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	components := assessorAt(t, now, AssessorConfig{
		Database: stubPinger{}, Chain: stubChain{id: 1}, ExpectedChainID: 1,
		Heartbeats: stubHeartbeats{}, SettlementEnabled: false,
	}).Components(context.Background())

	if got := stateOf(components, "settlement").State; got != StateDisabled {
		t.Errorf("settlement = %s, want %s", got, StateDisabled)
	}
	if got := overall(components); got != StateOperational {
		t.Errorf("overall = %s, want %s — a disabled capability is not an outage", got, StateOperational)
	}
}

// TestOutageOutranksDegraded pins the reduction: a service with both a dead
// dependency and a slow one is down, not degraded.
func TestOutageOutranksDegraded(t *testing.T) {
	got := overall([]Component{
		{Name: "settlement", State: StateDegraded},
		{Name: "database", State: StateOutage},
		{Name: "ethereum_rpc", State: StateOperational},
	})
	if got != StateOutage {
		t.Errorf("overall = %s, want %s", got, StateOutage)
	}
}

// TestPublicStatusHidesInternalDetail keeps connection strings, hostnames, and
// driver internals out of a public response. The error text goes to the log.
func TestPublicStatusHidesInternalDetail(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	internalError := "dial tcp 10.4.2.19:5432: connect: connection refused (user=eth402_runtime)"
	components := assessorAt(t, now, AssessorConfig{
		Database: stubPinger{err: errors.New(internalError)}, Chain: stubChain{id: 1},
		ExpectedChainID: 1, Heartbeats: stubHeartbeats{}, SettlementEnabled: true,
	}).Components(context.Background())

	for _, component := range components {
		if component.Detail == internalError {
			t.Errorf("%s leaked the underlying error into a public response", component.Name)
		}
	}
	if got := stateOf(components, "database").Detail; got != "unreachable" {
		t.Errorf("database detail = %q, want %q", got, "unreachable")
	}
}

// TestStatusIsProbedOnTheCacheSchedule matters because /stats and /status are
// public and unauthenticated: probing per request would let anyone outside drive
// database and RPC load by holding the endpoint open.
func TestStatusIsProbedOnTheCacheSchedule(t *testing.T) {
	start := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	counter := &countingHealth{}
	service := NewService(Config{
		Source: fakeSource{}, Health: counter, Started: start, TTL: time.Minute,
	})
	service.now = func() time.Time { return start }
	for range 20 {
		if _, err := service.Get(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if counter.calls != 1 {
		t.Errorf("probed %d times for 20 requests inside one TTL, want 1", counter.calls)
	}
	// Past the TTL it must probe again, or the page would freeze on a stale verdict
	// and report health through an outage.
	service.now = func() time.Time { return start.Add(2 * time.Minute) }
	if _, err := service.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if counter.calls != 2 {
		t.Errorf("probed %d times after the TTL expired, want 2", counter.calls)
	}
}

type countingHealth struct{ calls int }

func (c *countingHealth) Components(context.Context) []Component {
	c.calls++
	return []Component{{Name: "database", State: StateOperational}}
}

// TestVolumeIsWithheldByDefault covers the privacy posture. Aggregating does not
// anonymize a cumulative total: polling it yields its own deltas, and a delta
// spanning one settlement is that payment's exact amount.
func TestVolumeIsWithheldByDefault(t *testing.T) {
	start := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	source := fakeSource{Aggregate{
		TotalPaymentVolumeAtomic: "1234567", VolumeLast24hAtomic: "1234567",
		ConfirmedSettlements: 1, SettlementsLast24h: 1,
	}}
	withheld, err := NewService(Config{Source: source, Started: start}).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if withheld.TotalPaymentVolumeAtomic != "" || withheld.TotalPaymentVolumeUSDC != "" ||
		withheld.VolumeLast24hAtomic != "" {
		t.Errorf("volume must be withheld by default, got %+v", withheld)
	}
	// The counts stay: they carry no amount, and without them the page could not
	// report whether settlement is progressing at all.
	if withheld.ConfirmedSettlements != 1 {
		t.Errorf("confirmed settlements = %d, want 1", withheld.ConfirmedSettlements)
	}

	published, err := NewService(Config{
		Source: source, Started: start, PublishVolume: true,
	}).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if published.TotalPaymentVolumeAtomic != "1234567" || published.VolumeLast24hAtomic != "1234567" {
		t.Errorf("opting in must publish volume, got %+v", published)
	}
}

// TestServiceReportsTheAssessedStatus closes the gap between deriving a status and
// publishing it. The original defect was in the service, not the derivation: it
// wrote the constant "operational" into every response. A test that only checks
// overall() would still pass against that code.
func TestServiceReportsTheAssessedStatus(t *testing.T) {
	start := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	service := NewService(Config{
		Source: fakeSource{}, Started: start,
		Health: NewAssessor(AssessorConfig{
			Database: stubPinger{err: errors.New("refused")}, Chain: stubChain{id: 1},
			ExpectedChainID: 1, Heartbeats: stubHeartbeats{}, SettlementEnabled: true,
		}),
	})
	got, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StateOutage {
		t.Errorf("status = %q, want %q — the published status must follow the observations",
			got.Status, StateOutage)
	}
	if len(got.Components) == 0 {
		t.Error("components must be published so the status can be checked against evidence")
	}
}

// TestUnobservableStatusIsNotOperational guards the trap this design fell into
// once already: defaulting to healthy. With no health source there is nothing to
// reduce, and claiming "operational" would restate the original bug.
func TestUnobservableStatusIsNotOperational(t *testing.T) {
	if got := overall(nil); got == StateOperational {
		t.Errorf("overall(nil) = %q; an unobservable service is not a healthy one", got)
	}
	if got := overall([]Component{}); got != StateUnknown {
		t.Errorf("overall(empty) = %q, want %q", got, StateUnknown)
	}
	start := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	got, err := NewService(Config{Source: fakeSource{}, Started: start}).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == StateOperational {
		t.Errorf("a service with no health source published %q", got.Status)
	}
}
