package stats

import (
	"context"
	"sync"
	"time"

	ethx402 "github.com/ETH402/facilitator/internal/x402"
)

// SchemaVersion is 2 because the public response changed shape: volume figures are
// now omitted unless the operator opts in. See Config.PublishVolume.
const SchemaVersion = "2"

type Aggregate struct {
	RegisteredMerchants      int64
	VerifiedMerchants        int64
	TotalVerifications       int64
	SuccessfulVerifications  int64
	FailedVerifications      int64
	TotalSettlementRequests  int64
	ConfirmedSettlements     int64
	FailedSettlements        int64
	TotalPaymentVolumeAtomic string
	SettlementsLast24h       int64
	VolumeLast24hAtomic      string
	LastConfirmedBlock       uint64
	LatestIndexedBlock       uint64
}

type Response struct {
	SchemaVersion            string `json:"schema_version"`
	Service                  string `json:"service"`
	Version                  string `json:"version"`
	Network                  string `json:"network"`
	Asset                    string `json:"asset"`
	UptimeSeconds            int64  `json:"uptime_seconds"`
	RegisteredMerchants      int64  `json:"registered_merchants"`
	VerifiedMerchants        int64  `json:"verified_merchants"`
	TotalVerifications       int64  `json:"total_verifications"`
	SuccessfulVerifications  int64  `json:"successful_verifications"`
	FailedVerifications      int64  `json:"failed_verifications"`
	TotalSettlementRequests  int64  `json:"total_settlement_requests"`
	ConfirmedSettlements     int64  `json:"confirmed_settlements"`
	FailedSettlements        int64  `json:"failed_settlements"`
	TotalPaymentVolumeAtomic string `json:"total_payment_volume_atomic,omitempty"`
	TotalPaymentVolumeUSDC   string `json:"total_payment_volume_usdc,omitempty"`
	SettlementsLast24h       int64  `json:"settlements_last_24h"`
	VolumeLast24hAtomic      string `json:"volume_last_24h_atomic,omitempty"`
	LastConfirmedBlock       uint64 `json:"last_confirmed_block"`
	LatestIndexedBlock       uint64 `json:"latest_indexed_block"`
	ConfirmationLagBlocks    uint64 `json:"confirmation_lag_blocks"`

	// Status is derived from the observations in Components, never assumed. It was
	// previously the constant "operational", which meant the field reported health
	// during an outage — the one moment anybody reads it.
	Status     string      `json:"status"`
	Components []Component `json:"components,omitempty"`
}

type Source interface {
	AggregateStats(context.Context) (Aggregate, error)
}

// HealthSource observes the facilitator's dependencies. *Assessor satisfies it.
type HealthSource interface {
	Components(context.Context) []Component
}

type Service struct {
	source  Source
	health  HealthSource
	started time.Time
	ttl     time.Duration
	// publishVolume gates the settled-volume figures.
	//
	// They are withheld by default because aggregating does not anonymize them
	// here. A cumulative total polled over time yields its own deltas, and a delta
	// spanning a single settlement is that payment's exact amount — recoverable by
	// anyone, without authentication, and correlatable with USDC transfers in the
	// same window. The 24-hour window has the same problem at low volume, where the
	// "aggregate" is one payment. Rounding does not fix it: repeated polling
	// recovers the crossings. So this is an operator decision to publish business
	// figures, not a privacy-preserving default.
	publishVolume bool
	now           func() time.Time
	mu            sync.Mutex
	cached        Response
	expires       time.Time
}

// Config groups the service's options so adding one does not change every caller.
type Config struct {
	Source        Source
	Health        HealthSource
	Started       time.Time
	TTL           time.Duration
	PublishVolume bool
}

func NewService(cfg Config) *Service {
	return &Service{
		source: cfg.Source, health: cfg.Health, started: cfg.Started,
		ttl: cfg.TTL, publishVolume: cfg.PublishVolume, now: time.Now,
	}
}

func (s *Service) Get(ctx context.Context) (Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if !s.expires.IsZero() && now.Before(s.expires) {
		cached := s.cached
		cached.UptimeSeconds = max(0, int64(now.Sub(s.started).Seconds()))
		return cached, nil
	}
	// Health is probed on the same cached schedule as the aggregates. That is the
	// point of the cache: /stats is public and unauthenticated, so probing per
	// request would let anyone drive database and RPC load from outside.
	components := s.componentsOf(ctx)
	a, err := s.source.AggregateStats(ctx)
	if err != nil {
		return Response{}, err
	}
	total, err := ethx402.FormatUSDC(zero(a.TotalPaymentVolumeAtomic))
	if err != nil {
		return Response{}, err
	}
	lag := uint64(0)
	if a.LatestIndexedBlock > a.LastConfirmedBlock {
		lag = a.LatestIndexedBlock - a.LastConfirmedBlock
	}
	response := Response{
		SchemaVersion: SchemaVersion, Service: "ETH402", Version: "0.2.0",
		Network: "eip155:1", Asset: "USDC", UptimeSeconds: max(0, int64(now.Sub(s.started).Seconds())),
		RegisteredMerchants: a.RegisteredMerchants, VerifiedMerchants: a.VerifiedMerchants,
		TotalVerifications: a.TotalVerifications, SuccessfulVerifications: a.SuccessfulVerifications,
		FailedVerifications: a.FailedVerifications, TotalSettlementRequests: a.TotalSettlementRequests,
		ConfirmedSettlements: a.ConfirmedSettlements, FailedSettlements: a.FailedSettlements,
		TotalPaymentVolumeAtomic: zero(a.TotalPaymentVolumeAtomic), TotalPaymentVolumeUSDC: total,
		SettlementsLast24h: a.SettlementsLast24h, VolumeLast24hAtomic: zero(a.VolumeLast24hAtomic),
		LastConfirmedBlock: a.LastConfirmedBlock, LatestIndexedBlock: a.LatestIndexedBlock,
		ConfirmationLagBlocks: lag,
		Status:                overall(components), Components: components,
	}
	if !s.publishVolume {
		response.TotalPaymentVolumeAtomic = ""
		response.TotalPaymentVolumeUSDC = ""
		response.VolumeLast24hAtomic = ""
	}
	s.cached, s.expires = response, now.Add(s.ttl)
	return response, nil
}

func (s *Service) componentsOf(ctx context.Context) []Component {
	if s.health == nil {
		return nil
	}
	return s.health.Components(ctx)
}

func zero(value string) string {
	if value == "" {
		return "0"
	}
	return value
}
