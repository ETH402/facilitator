package settlement

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"testing"
	"time"
)

type fakeBalance struct {
	wei  *big.Int
	err  error
	read int
}

func (f *fakeBalance) Balance(context.Context, string) (*big.Int, error) {
	f.read++
	return f.wei, f.err
}

type fakeRecorder struct {
	wei    *big.Int
	at     time.Time
	errors int
	sets   int
}

func (f *fakeRecorder) SetSignerBalance(wei *big.Int, at time.Time) {
	f.wei, f.at, f.sets = wei, at, f.sets+1
}
func (f *fakeRecorder) IncSignerBalanceError() { f.errors++ }

func balanceService(address string) *Service {
	return NewService(nil, nil, nil, Config{
		SignerAddress: address, WorkerInterval: time.Hour,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestBalanceWorkerPublishesTheReading(t *testing.T) {
	chain := &fakeBalance{wei: big.NewInt(50_000_000_000_000_000)} // 0.05 ETH
	recorder := &fakeRecorder{}
	worker := balanceService("0x00000000000000000000000000000000000000b2").
		BalanceWorker(chain, recorder)
	worker.observe(context.Background())
	if recorder.sets != 1 || recorder.wei.Cmp(chain.wei) != 0 {
		t.Fatalf("sets=%d wei=%v", recorder.sets, recorder.wei)
	}
	if recorder.at.IsZero() {
		t.Fatal("no read timestamp recorded; staleness would be undetectable")
	}
}

// A failed read must not overwrite the balance. Publishing zero would look exactly
// like a drained account, and refreshing the timestamp would make a stale figure
// look current — staleness is the signal that reads are failing.
func TestBalanceWorkerFailedReadPreservesLastValue(t *testing.T) {
	chain := &fakeBalance{wei: big.NewInt(42)}
	recorder := &fakeRecorder{}
	worker := balanceService("0x00000000000000000000000000000000000000b2").
		BalanceWorker(chain, recorder)
	worker.observe(context.Background())
	first := recorder.at

	chain.wei, chain.err = nil, errors.New("rpc unavailable")
	worker.observe(context.Background())

	if recorder.errors != 1 {
		t.Fatalf("error count = %d, want 1", recorder.errors)
	}
	if recorder.sets != 1 {
		t.Fatalf("a failed read published a value (sets=%d)", recorder.sets)
	}
	if recorder.wei.Cmp(big.NewInt(42)) != 0 {
		t.Fatalf("last good balance was overwritten: %v", recorder.wei)
	}
	if !recorder.at.Equal(first) {
		t.Fatal("timestamp refreshed on a failed read; staleness would be hidden")
	}
}

// A cancelled context is shutdown, not a fault, and must not be counted as one.
func TestBalanceWorkerIgnoresCancellation(t *testing.T) {
	chain := &fakeBalance{err: context.Canceled}
	recorder := &fakeRecorder{}
	worker := balanceService("0x00000000000000000000000000000000000000b2").
		BalanceWorker(chain, recorder)
	worker.observe(context.Background())
	if recorder.errors != 0 {
		t.Fatalf("cancellation counted as a read error (%d)", recorder.errors)
	}
}

// Nothing to watch must yield no worker, so main can start it unconditionally.
func TestBalanceWorkerAbsentWithoutASigner(t *testing.T) {
	if w := balanceService("").BalanceWorker(&fakeBalance{}, &fakeRecorder{}); w != nil {
		t.Fatal("a worker was built with no signer address")
	}
	if w := balanceService("0xabc").BalanceWorker(nil, &fakeRecorder{}); w != nil {
		t.Fatal("a worker was built with no chain")
	}
	// Run on a nil worker must be safe, since main calls it unconditionally.
	var nilWorker *BalanceWorker
	nilWorker.Run(context.Background())
}
