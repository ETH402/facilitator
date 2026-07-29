package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrFairUseExceeded means the merchant has spent its allowance for the window.
var ErrFairUseExceeded = errors.New("merchant fair-use allowance exceeded for the current window")

// FairUse is the outcome of one accounted request.
type FairUse struct {
	Limit     int64
	Used      int64
	ResetsAt  time.Time
	Exceeded  bool
	Remaining int64
}

// CountMerchantRequest accounts one authenticated request against a merchant's
// fair-use allowance and reports whether it is permitted.
//
// It counts *before* deciding, in one statement, so the decision is made against a
// committed number rather than a snapshot another instance could be racing. A
// read-then-write would let two instances both observe used == limit-1 and both
// allow; the returning-upsert makes the increment and the read the same operation,
// so concurrency is resolved by the row lock rather than by hoping.
//
// The consequence is that refused requests still count. That is deliberate: a
// caller hammering past its limit is exactly the caller whose requests should keep
// extending its own lockout, and not counting them would make the limit free to
// exceed by ignoring the 429s.
func CountMerchantRequest(ctx context.Context, db Queryer, merchantID string, limit int64, window time.Duration, now time.Time) (FairUse, error) {
	if limit <= 0 || window <= 0 {
		return FairUse{}, fmt.Errorf("fair-use limit and window must be positive, got %d and %s", limit, window)
	}
	// Tumbling window: truncation makes the window boundary identical on every
	// instance without coordination, which a per-instance start time would not.
	start := now.Truncate(window)
	var used int64
	err := db.QueryRow(ctx, `
INSERT INTO merchant_usage (merchant_id, window_start, requests)
VALUES ($1, $2, 1)
ON CONFLICT (merchant_id, window_start)
DO UPDATE SET requests = merchant_usage.requests + 1
RETURNING requests`, merchantID, start).Scan(&used)
	if err != nil {
		return FairUse{}, fmt.Errorf("account merchant request: %w", err)
	}
	return FairUse{
		Limit:     limit,
		Used:      used,
		ResetsAt:  start.Add(window),
		Exceeded:  used > limit,
		Remaining: max(0, limit-used),
	}, nil
}

// CountMerchantRequest on the store accounts against the pool. The package-level
// function takes a Queryer so tests and transactions can use it too.
func (s *Store) CountMerchantRequest(ctx context.Context, merchantID string, limit int64, window time.Duration, now time.Time) (FairUse, error) {
	return CountMerchantRequest(ctx, s.Pool, merchantID, limit, window, now)
}

// PruneMerchantUsage deletes accounting rows for windows that have fully elapsed.
//
// Retaining them would turn a fair-use counter into an indefinite per-merchant
// activity log, which docs/PRIVACY.md would then have to account for. Keeping one
// extra window of history means a clock skew between instances cannot delete a
// window that is still being written to.
func PruneMerchantUsage(ctx context.Context, db Queryer, window time.Duration, now time.Time) (int64, error) {
	if window <= 0 {
		return 0, fmt.Errorf("fair-use window must be positive, got %s", window)
	}
	tag, err := db.Exec(ctx, `
DELETE FROM merchant_usage WHERE window_start < $1`, now.Add(-2*window).Truncate(window))
	if err != nil {
		return 0, fmt.Errorf("prune merchant usage: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Queryer is the subset of a pool or transaction this file needs, so callers can
// pass either.
type Queryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}
