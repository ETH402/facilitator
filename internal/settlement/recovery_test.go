package settlement

import (
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/ethereum"
	"golang.org/x/crypto/sha3"
)

func keccakHex(data []byte) string {
	keccak := sha3.NewLegacyKeccak256()
	keccak.Write(data)
	return hex.EncodeToString(keccak.Sum(nil))
}

// testSigHash is the stand-in digest the fake signer commits to; recovery only
// ever compares it for equality with the stored value.
var testSigHash = [32]byte{0x42}

func testSigHashHex() string { return hex.EncodeToString(testSigHash[:]) }

// ambiguousWork is a signed-but-unrecorded broadcast with every recovery
// field populated; rawHash matches the fake signer's raw bytes.
func ambiguousWork(raw []byte) Work {
	work := pendingWork()
	work.TransactionStatus = "ambiguous"
	work.TxHash = ""
	work.RawHash = keccakHex(raw)
	work.Sighash = testSigHashHex()
	work.SignerAddress = "0x00000000000000000000000000000000000000b2"
	work.GasLimit = 100000
	work.MaxFeePerGas = "3000000000"
	work.MaxPriorityFeePerGas = "1000000000"
	work.TransactionUpdatedAt = time.Now()
	return work
}

func TestResolveAmbiguousReceiptFound(t *testing.T) {
	raw := []byte("raw-tx")
	work := ambiguousWork(raw)
	txHash := "0x" + work.RawHash
	store := &fakeStore{work: work}
	chain := fakeChain{receipts: map[string]*ethereum.Receipt{
		txHash: {Status: 1, BlockNumber: 100, BlockHash: "0xblock"},
	}}
	service := newTestService(store, fakeSigner{raw: raw}, chain)
	if err := service.recoverPayment(context.Background(), work.PaymentID, "test"); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if store.recoveredTxHash != txHash {
		t.Fatalf("recovered hash = %q, want %q", store.recoveredTxHash, txHash)
	}
}

func TestResolveAmbiguousPendingFound(t *testing.T) {
	raw := []byte("raw-tx")
	work := ambiguousWork(raw)
	txHash := "0x" + work.RawHash
	store := &fakeStore{work: work}
	chain := fakeChain{transactions: map[string]*ethereum.ChainTransaction{
		txHash: {Hash: txHash},
	}}
	service := newTestService(store, fakeSigner{raw: raw}, chain)
	if err := service.recoverPayment(context.Background(), work.PaymentID, "test"); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if store.recoveredTxHash != txHash {
		t.Fatalf("recovered hash = %q, want %q", store.recoveredTxHash, txHash)
	}
}

func TestResolveAmbiguousGraceWindow(t *testing.T) {
	raw := []byte("raw-tx")
	work := ambiguousWork(raw) // TransactionUpdatedAt = now: inside the grace window.
	store := &fakeStore{work: work}
	chain := fakeChain{sendErr: context.DeadlineExceeded}
	service := newTestService(store, fakeSigner{raw: raw}, chain)
	if err := service.recoverPayment(context.Background(), work.PaymentID, "test"); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if store.recoveredTxHash != "" {
		t.Fatalf("recovered inside grace window: %q", store.recoveredTxHash)
	}
}

func TestResolveAmbiguousRebroadcastIdentical(t *testing.T) {
	raw := []byte("raw-tx")
	work := ambiguousWork(raw)
	work.TransactionUpdatedAt = time.Now().Add(-time.Hour) // Past the grace window.
	store := &fakeStore{work: work}
	chain := fakeChain{txHash: "0x" + work.RawHash}
	service := newTestService(store, fakeSigner{raw: raw, sigHash: testSigHash}, chain)
	if err := service.recoverPayment(context.Background(), work.PaymentID, "test"); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if store.recoveredTxHash != "0x"+work.RawHash {
		t.Fatalf("recovered hash = %q", store.recoveredTxHash)
	}
	if store.ambiguousReplaced {
		t.Fatal("identical re-sign must recover in place, not record a replacement")
	}
}

// TestResolveAmbiguousRebroadcastNondeterministic covers the production
// signer: Cloud KMS randomizes the ECDSA nonce, so the re-signed bytes differ
// even though the sighash proves the transaction identical. The fresh
// signature must be recorded replacement-shaped — before broadcasting — so the
// network mining either signature resolves the payment.
func TestResolveAmbiguousRebroadcastNondeterministic(t *testing.T) {
	work := ambiguousWork([]byte("raw-tx"))
	work.TransactionUpdatedAt = time.Now().Add(-time.Hour)
	store := &fakeStore{work: work}
	chain := fakeChain{txHash: "0x" + keccakHex([]byte("raw-tx-resigned"))}
	service := newTestService(store, fakeSigner{raw: []byte("raw-tx-resigned"), sigHash: testSigHash}, chain)
	if err := service.recoverPayment(context.Background(), work.PaymentID, "test"); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if store.recoveredTxHash != "" {
		t.Fatalf("a differing re-sign must not recover under the old hash: %q", store.recoveredTxHash)
	}
	if !store.ambiguousReplaced {
		t.Fatal("re-signed transaction was not recorded as a replacement")
	}
	wantHash := keccakHex([]byte("raw-tx-resigned"))
	if store.ambReplacement.TxHash != "0x"+wantHash || store.ambReplacement.RawHash != wantHash {
		t.Fatalf("replacement hashes = %q / %q, want %q", store.ambReplacement.TxHash, store.ambReplacement.RawHash, wantHash)
	}
	if store.ambReplacement.Nonce != work.Nonce ||
		store.ambReplacement.MaxFee != work.MaxFeePerGas ||
		store.ambReplacement.PriorityFee != work.MaxPriorityFeePerGas ||
		store.ambReplacement.GasLimit != work.GasLimit {
		t.Fatalf("replacement terms = %+v, want the recorded transaction's", store.ambReplacement)
	}
}

// The replacement is recorded before the broadcast attempt: a failed send must
// leave the durable row behind rather than returning the payment to the
// ambiguous limbo it just left.
func TestResolveAmbiguousNondeterministicBroadcastFailsAfterRecording(t *testing.T) {
	work := ambiguousWork([]byte("raw-tx"))
	work.TransactionUpdatedAt = time.Now().Add(-time.Hour)
	store := &fakeStore{work: work}
	chain := fakeChain{sendErr: context.DeadlineExceeded}
	service := newTestService(store, fakeSigner{raw: []byte("raw-tx-resigned"), sigHash: testSigHash}, chain)
	err := service.recoverPayment(context.Background(), work.PaymentID, "test")
	if err == nil || !strings.Contains(err.Error(), "broadcast re-signed transaction") {
		t.Fatalf("err = %v", err)
	}
	if !store.ambiguousReplaced {
		t.Fatal("replacement must be recorded before the broadcast attempt")
	}
}

func TestResolveAmbiguousSighashMismatchRefuses(t *testing.T) {
	work := ambiguousWork([]byte("raw-tx"))
	work.Sighash = strings.Repeat("00", 32) // Corrupt: re-signing reproduces a different sighash.
	work.TransactionUpdatedAt = time.Now().Add(-time.Hour)
	store := &fakeStore{work: work}
	chain := fakeChain{}
	service := newTestService(store, fakeSigner{raw: []byte("raw-tx"), sigHash: testSigHash}, chain)
	err := service.recoverPayment(context.Background(), work.PaymentID, "test")
	if err == nil || !strings.Contains(err.Error(), "sighash") {
		t.Fatalf("err = %v", err)
	}
	if store.recoveredTxHash != "" || store.ambiguousReplaced {
		t.Fatalf("recovered despite sighash mismatch: %q / %v", store.recoveredTxHash, store.ambiguousReplaced)
	}
}

func TestResolveAmbiguousHashMismatchRefuses(t *testing.T) {
	work := ambiguousWork([]byte("raw-tx"))
	work.Sighash = ""                       // Pre-000006 row: identity falls back to the raw hash.
	work.RawHash = strings.Repeat("00", 32) // Corrupt: re-signing reproduces a different hash.
	work.TransactionUpdatedAt = time.Now().Add(-time.Hour)
	store := &fakeStore{work: work}
	chain := fakeChain{}
	service := newTestService(store, fakeSigner{raw: []byte("raw-tx")}, chain)
	err := service.recoverPayment(context.Background(), work.PaymentID, "test")
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v", err)
	}
	if store.recoveredTxHash != "" {
		t.Fatalf("recovered despite hash mismatch: %q", store.recoveredTxHash)
	}
}

func TestResolveAmbiguousMissingFeesStaysManual(t *testing.T) {
	work := ambiguousWork([]byte("raw-tx"))
	work.MaxFeePerGas = "" // Pre-000004 row: on-chain lookup only.
	work.TransactionUpdatedAt = time.Now().Add(-time.Hour)
	store := &fakeStore{work: work}
	service := newTestService(store, fakeSigner{raw: []byte("raw-tx")}, fakeChain{})
	err := service.recoverPayment(context.Background(), work.PaymentID, "test")
	if err == nil || !strings.Contains(err.Error(), "stored fee fields") {
		t.Fatalf("err = %v", err)
	}
	if store.recoveredTxHash != "" {
		t.Fatalf("recovered without stored fees: %q", store.recoveredTxHash)
	}
}

func stuckWork() Work {
	work := pendingWork()
	work.TransactionStatus = "broadcast"
	work.TxHash = "0x" + strings.Repeat("cc", 32)
	work.RawHash = strings.Repeat("cc", 32)
	work.SignerAddress = "0x00000000000000000000000000000000000000b2"
	work.GasLimit = 100000
	work.MaxFeePerGas = "2000000000"
	work.MaxPriorityFeePerGas = "1000000000"
	work.BroadcastAttemptedAt = time.Now().Add(-time.Hour)
	return work
}

func TestReplaceStuckBroadcastsReplacement(t *testing.T) {
	work := stuckWork()
	store := &fakeStore{work: work}
	chain := fakeChain{txHash: "0x" + strings.Repeat("dd", 32)}
	service := newTestService(store, fakeSigner{raw: []byte("replacement-raw")}, chain)
	if err := service.recoverPayment(context.Background(), work.PaymentID, "test"); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if !store.replaced {
		t.Fatal("replacement was not recorded")
	}
	// baseFee 1 gwei, old tip 1 gwei bumped to 1.125 gwei:
	// maxFee = 2*1 + 1.125 = 3.125 gwei, above the 110% floor of the old
	// 2 gwei cap and beneath the 30 gwei ceiling.
	if store.replacement.MaxFee != "3125000000" {
		t.Fatalf("replacement max fee = %q", store.replacement.MaxFee)
	}
	if store.replacement.PriorityFee != "1125000000" {
		t.Fatalf("replacement priority fee = %q", store.replacement.PriorityFee)
	}
	if store.replacement.Nonce != work.Nonce {
		t.Fatalf("replacement nonce = %d, want %d", store.replacement.Nonce, work.Nonce)
	}
}

func TestReplaceStuckNoHeadroomWaits(t *testing.T) {
	work := stuckWork()
	work.MaxFeePerGas = "3000000000"
	work.MaxPriorityFeePerGas = "1000000000"
	store := &fakeStore{work: work}
	// Ceiling 3 gwei leaves no room above the old 3 gwei max fee.
	cfg := testConfig()
	cfg.MaxFeePerGas = "3000000000"
	service := NewService(store, fakeSigner{raw: []byte("replacement-raw")}, fakeChain{}, cfg, nil)
	if err := service.recoverPayment(context.Background(), work.PaymentID, "test"); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if store.replaced {
		t.Fatal("replacement recorded despite no fee headroom")
	}
}

func TestReplaceStuckInsideWindowLeftAlone(t *testing.T) {
	work := stuckWork()
	work.BroadcastAttemptedAt = time.Now() // Not yet stale.
	store := &fakeStore{work: work}
	service := newTestService(store, fakeSigner{raw: []byte("replacement-raw")}, fakeChain{})
	if err := service.recoverPayment(context.Background(), work.PaymentID, "test"); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if store.replaced {
		t.Fatal("replacement recorded inside the replacement window")
	}
}

func TestObserveReplacementsLanded(t *testing.T) {
	txHash := "0x" + strings.Repeat("cc", 32)
	for _, status := range []uint64{1, 0} {
		store := &fakeStore{
			replacedPending: []TrackedTransaction{{PaymentID: "payment-1", TransactionID: "tx-1", TxHash: txHash}},
		}
		chain := fakeChain{receipts: map[string]*ethereum.Receipt{
			txHash: {Status: status, BlockNumber: 100, BlockHash: "0xblock"},
		}}
		service := newTestService(store, fakeSigner{}, chain)
		worker := service.RecoveryWorker()
		worker.observeReplacements(context.Background())
		if !store.landed {
			t.Fatalf("status %d: landed original not recorded", status)
		}
		if store.landedSucceeded != (status == 1) {
			t.Fatalf("status %d: succeeded = %v", status, store.landedSucceeded)
		}
	}
}

func TestFillNonceGapBroadcasts(t *testing.T) {
	raw := []byte("gap-filler-raw")
	work := pendingWork()
	store := &fakeStore{gapWorks: []Work{work}}
	chain := fakeChain{txHash: "0x" + keccakHex(raw)}
	service := newTestService(store, fakeSigner{raw: raw}, chain)
	worker := service.RecoveryWorker()
	worker.fillNonceGaps(context.Background())
	if store.gapFillerTxHash != "0x"+keccakHex(raw) {
		t.Fatalf("gap filler hash = %q", store.gapFillerTxHash)
	}
	if string(store.gapFillerRaw) != string(raw) {
		t.Fatalf("prepared raw = %q, want %q", store.gapFillerRaw, raw)
	}
}

func TestObserveGapFillerRebroadcastsExactPreparedBytes(t *testing.T) {
	raw := []byte("gap-filler-raw")
	txHash := "0x" + keccakHex(raw)
	store := &fakeStore{gapFillers: []TrackedTransaction{{
		PaymentID: "payment-1", TransactionID: "tx-1",
		TxHash: txHash, RawTransaction: raw,
	}}}
	var sent string
	chain := fakeChain{
		txHash: txHash, sentRaw: &sent,
		receipts:     map[string]*ethereum.Receipt{txHash: nil},
		transactions: map[string]*ethereum.ChainTransaction{},
	}
	service := newTestService(store, fakeSigner{}, chain)
	worker := service.RecoveryWorker()
	worker.observeGapFillers(context.Background())
	if sent != "0x"+hex.EncodeToString(raw) {
		t.Fatalf("sent raw = %q", sent)
	}
}

func TestObserveGapFillers(t *testing.T) {
	txHash := "0x" + strings.Repeat("ee", 32)
	store := &fakeStore{
		gapFillers: []TrackedTransaction{{PaymentID: "payment-1", TransactionID: "tx-1", TxHash: txHash}},
	}
	chain := fakeChain{receipts: map[string]*ethereum.Receipt{
		txHash: {Status: 0, BlockNumber: 100, GasUsed: 21000, EffectiveGasPrice: "1000000000"},
	}}
	service := newTestService(store, fakeSigner{}, chain)
	worker := service.RecoveryWorker()
	worker.observeGapFillers(context.Background())
	if !store.gapFillerResolved {
		t.Fatal("reverted gap filler was not resolved")
	}
}

func TestObserveGapFillersSuccessLeftForHumans(t *testing.T) {
	txHash := "0x" + strings.Repeat("ee", 32)
	store := &fakeStore{
		gapFillers: []TrackedTransaction{{PaymentID: "payment-1", TransactionID: "tx-1", TxHash: txHash}},
	}
	chain := fakeChain{receipts: map[string]*ethereum.Receipt{
		txHash: {Status: 1, BlockNumber: 100},
	}}
	service := newTestService(store, fakeSigner{}, chain)
	worker := service.RecoveryWorker()
	worker.observeGapFillers(context.Background())
	if store.gapFillerResolved {
		t.Fatal("successful gap filler was auto-resolved; it must be left for investigation")
	}
}

func TestConfirmationReorgReturnsToBroadcast(t *testing.T) {
	work := pendingWork()
	work.TransactionStatus = "confirming"
	work.TxHash = "0x" + strings.Repeat("cc", 32)
	store := &fakeStore{work: work}
	chain := fakeChain{} // No canonical receipt: the block was reorged out.
	service := newTestService(store, fakeSigner{}, chain)
	if err := service.Confirmation(context.Background(), work.PaymentID, "test"); err != nil {
		t.Fatalf("confirmation: %v", err)
	}
	if !store.reorgedOut {
		t.Fatal("reorged transaction was not returned to broadcast")
	}
}

// A gap filler the chain accepted must be escalated exactly once. It previously
// logged the same anomaly on every tick and never left the observation list.
func TestSucceededGapFillerEscalatesOnceAndStopsBeingObserved(t *testing.T) {
	store := &fakeStore{gapFillers: []TrackedTransaction{
		{PaymentID: "payment-1", TransactionID: "tx-1", TxHash: "0xabc"},
	}}
	chain := &fakeChain{receipts: map[string]*ethereum.Receipt{
		"0xabc": {Status: 1, BlockNumber: 99, BlockHash: "0xblock", GasUsed: 51000, EffectiveGasPrice: "30000000000"},
	}}
	service := NewService(store, nil, chain, Config{
		WorkerInterval: time.Hour, LeaseDuration: time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	worker := service.RecoveryWorker()

	worker.observeGapFillers(context.Background())
	if store.gapFillerEscalated != 1 {
		t.Fatalf("escalations = %d, want 1", store.gapFillerEscalated)
	}
	if store.gapFillerResolved {
		t.Fatal("a succeeded filler must not be resolved as reverted")
	}
	// A second pass must find nothing: the payment left expired.
	worker.observeGapFillers(context.Background())
	if store.gapFillerEscalated != 1 {
		t.Fatalf("escalations after second pass = %d, want 1", store.gapFillerEscalated)
	}
}
