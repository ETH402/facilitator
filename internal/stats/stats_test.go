package stats

import (
	"context"
	"testing"
	"time"
)

type fakeSource struct{ value Aggregate }

func (f fakeSource) AggregateStats(context.Context) (Aggregate, error) { return f.value, nil }

func TestAggregation(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	s := NewService(fakeSource{Aggregate{
		RegisteredMerchants: 2, VerifiedMerchants: 1, ConfirmedSettlements: 3,
		TotalPaymentVolumeAtomic: "1234567", LastConfirmedBlock: 100, LatestIndexedBlock: 105,
	}}, start, 0)
	s.now = func() time.Time { return start.Add(time.Minute) }
	got, err := s.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalPaymentVolumeUSDC != "1.234567" || got.ConfirmationLagBlocks != 5 || got.UptimeSeconds != 60 {
		t.Fatalf("unexpected aggregate: %+v", got)
	}
}
