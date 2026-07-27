package stats

import (
	"context"
	"sync"
	"time"

	ethx402 "github.com/ETH402/facilitator/internal/x402"
)

const SchemaVersion = "1"

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
	TotalPaymentVolumeAtomic string `json:"total_payment_volume_atomic"`
	TotalPaymentVolumeUSDC   string `json:"total_payment_volume_usdc"`
	SettlementsLast24h       int64  `json:"settlements_last_24h"`
	VolumeLast24hAtomic      string `json:"volume_last_24h_atomic"`
	LastConfirmedBlock       uint64 `json:"last_confirmed_block"`
	LatestIndexedBlock       uint64 `json:"latest_indexed_block"`
	ConfirmationLagBlocks    uint64 `json:"confirmation_lag_blocks"`
	Status                   string `json:"status"`
}

type Source interface {
	AggregateStats(context.Context) (Aggregate, error)
}

type Service struct {
	source  Source
	started time.Time
	ttl     time.Duration
	now     func() time.Time
	mu      sync.Mutex
	cached  Response
	expires time.Time
}

func NewService(source Source, started time.Time, ttl time.Duration) *Service {
	return &Service{source: source, started: started, ttl: ttl, now: time.Now}
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
		SchemaVersion: SchemaVersion, Service: "ETH402", Version: "0.1.0",
		Network: "eip155:1", Asset: "USDC", UptimeSeconds: max(0, int64(now.Sub(s.started).Seconds())),
		RegisteredMerchants: a.RegisteredMerchants, VerifiedMerchants: a.VerifiedMerchants,
		TotalVerifications: a.TotalVerifications, SuccessfulVerifications: a.SuccessfulVerifications,
		FailedVerifications: a.FailedVerifications, TotalSettlementRequests: a.TotalSettlementRequests,
		ConfirmedSettlements: a.ConfirmedSettlements, FailedSettlements: a.FailedSettlements,
		TotalPaymentVolumeAtomic: zero(a.TotalPaymentVolumeAtomic), TotalPaymentVolumeUSDC: total,
		SettlementsLast24h: a.SettlementsLast24h, VolumeLast24hAtomic: zero(a.VolumeLast24hAtomic),
		LastConfirmedBlock: a.LastConfirmedBlock, LatestIndexedBlock: a.LatestIndexedBlock,
		ConfirmationLagBlocks: lag, Status: "operational",
	}
	s.cached, s.expires = response, now.Add(s.ttl)
	return response, nil
}

func zero(value string) string {
	if value == "" {
		return "0"
	}
	return value
}
