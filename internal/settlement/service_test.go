package settlement

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/config"
	"github.com/ETH402/facilitator/internal/ethereum"
	"github.com/ETH402/facilitator/internal/signer"
	"github.com/x402-foundation/x402/go/v2/types"
)

type fakeStore struct {
	work       Work
	intent     Intent
	intentErr  error
	claimErr   error
	renewErr   error
	releaseErr error
	leases     []Lease

	signedRawHash   string
	signedSighash   string
	broadcastTxHash string
	ambiguous       bool
	expired         bool
	confirming      bool
	confirmed       bool
	reverted        bool
	released        int
	renewed         int

	recoveredTxHash    string
	replaced           bool
	replacement        Replacement
	ambiguousReplaced  bool
	ambReplacement     Replacement
	ambiguousRetries   int
	landed             bool
	landedSucceeded    bool
	reorgedOut         bool
	replacedPending    []TrackedTransaction
	gapWorks           []Work
	gapFillers         []TrackedTransaction
	stuckGapWorks      []Work
	gapFillerTxHash    string
	gapFillerRaw       []byte
	gapFillerReplaced  bool
	gapFillerBump      Replacement
	gapFillerBumpRaw   []byte
	gapFillerResolved  bool
	unsettleable       int
	gapFillerEscalated int
}

func (f *fakeStore) CreateSettlementIntent(context.Context, IntentRequest) (Intent, error) {
	return f.intent, f.intentErr
}

func (f *fakeStore) ClaimPayment(_ context.Context, paymentID, worker string, duration time.Duration, now time.Time) (Lease, error) {
	if f.claimErr != nil {
		return Lease{}, f.claimErr
	}
	return Lease{PaymentID: paymentID, Worker: worker, Until: now.Add(duration)}, nil
}

func (f *fakeStore) ClaimPayments(context.Context, ClaimRequest) ([]Lease, error) {
	return f.leases, nil
}

func (f *fakeStore) RenewLease(_ context.Context, _ string, _ string, now time.Time, duration time.Duration) (time.Time, error) {
	f.renewed++
	if f.renewErr != nil {
		return time.Time{}, f.renewErr
	}
	return now.Add(duration), nil
}

func (f *fakeStore) ReleaseLease(context.Context, string, string) error {
	f.released++
	return f.releaseErr
}

func (f *fakeStore) LoadSettlementWork(context.Context, string) (Work, error) {
	return f.work, nil
}

func (f *fakeStore) MarkTxSigned(_ context.Context, _, rawHash, sighash string, _ uint64, _, _ string) error {
	f.signedRawHash = rawHash
	f.signedSighash = sighash
	return nil
}

func (f *fakeStore) MarkTxBroadcast(_ context.Context, _, _, txHash, _ string) error {
	f.broadcastTxHash = txHash
	return nil
}

func (f *fakeStore) MarkTxAmbiguous(context.Context, string, string, string) error {
	f.ambiguous = true
	return nil
}

func (f *fakeStore) MarkIntentExpired(context.Context, string, string, string) error {
	f.expired = true
	return nil
}

func (f *fakeStore) MarkTxConfirming(context.Context, string, string, uint64, string, string) error {
	f.confirming = true
	return nil
}

func (f *fakeStore) MarkTxConfirmed(context.Context, string, string, uint64, string, uint64, string, string) error {
	f.confirmed = true
	return nil
}

func (f *fakeStore) MarkTxReverted(context.Context, string, string, uint64, string, string) error {
	f.reverted = true
	return nil
}

func (f *fakeStore) MarkTxRecoveredBroadcast(_ context.Context, _, _, txHash, _ string) error {
	f.recoveredTxHash = txHash
	return nil
}

func (f *fakeStore) MarkTxReplaced(_ context.Context, _, _ string, replacement Replacement, _ string) error {
	f.replaced = true
	f.replacement = replacement
	return nil
}

func (f *fakeStore) MarkTxAmbiguousReplaced(_ context.Context, _, _ string, replacement Replacement, _ string) error {
	f.ambiguousReplaced = true
	f.ambReplacement = replacement
	return nil
}

func (f *fakeStore) MarkAmbiguousRetry(context.Context, string, string) error {
	f.ambiguousRetries++
	return nil
}

func (f *fakeStore) MarkReplacementLanded(_ context.Context, _, _ string, succeeded bool, _ uint64, _ string, _ uint64, _, _ string) error {
	f.landed = true
	f.landedSucceeded = succeeded
	return nil
}

func (f *fakeStore) MarkTxReorgedOut(context.Context, string, string, string) error {
	f.reorgedOut = true
	return nil
}

func (f *fakeStore) ListReplacedPending(context.Context) ([]TrackedTransaction, error) {
	return f.replacedPending, nil
}

func (f *fakeStore) ListDroppedBlockingGaps(context.Context, string, time.Time) ([]Work, error) {
	return f.gapWorks, nil
}

func (f *fakeStore) ListGapFillers(context.Context) ([]TrackedTransaction, error) {
	return f.gapFillers, nil
}

func (f *fakeStore) ListStuckGapFillers(context.Context, string, time.Duration) ([]Work, error) {
	return f.stuckGapWorks, nil
}

func (f *fakeStore) MarkGapFillerReplaced(_ context.Context, _, _ string, replacement Replacement, raw []byte) error {
	f.gapFillerReplaced = true
	f.gapFillerBump = replacement
	f.gapFillerBumpRaw = append([]byte(nil), raw...)
	return nil
}

func (f *fakeStore) MarkGapFillerPrepared(_ context.Context, _, _, txHash string, raw []byte, _ uint64, _, _ string) error {
	f.gapFillerTxHash = txHash
	f.gapFillerRaw = append([]byte(nil), raw...)
	return nil
}

func (f *fakeStore) MarkGapFillerResolved(context.Context, string, uint64, string) error {
	f.gapFillerResolved = true
	return nil
}

func (f *fakeStore) MarkIntentUnsettleable(context.Context, string, string, string) error {
	f.unsettleable++
	return nil
}

func (f *fakeStore) MarkGapFillerSucceeded(_ context.Context, _, _ string, _ uint64, _ string, _ uint64, _, _ string) error {
	f.gapFillerEscalated++
	// Escalation moves the payment out of expired, so the next listing drops it.
	f.gapFillers = nil
	return nil
}

type fakeSigner struct {
	raw     []byte
	sigHash [32]byte
	err     error
}

func (f fakeSigner) Address(context.Context) (string, error) {
	return "0x00000000000000000000000000000000000000b2", nil
}

func (f fakeSigner) SignTransaction(context.Context, signer.Transaction) (signer.SignedTransaction, error) {
	return signer.SignedTransaction{Raw: f.raw, SigHash: f.sigHash}, f.err
}

type fakeChain struct {
	txHash       string
	sendErr      error
	sentRaw      *string
	receipt      *ethereum.Receipt
	receipts     map[string]*ethereum.Receipt
	transactions map[string]*ethereum.ChainTransaction
	block        uint64
	blockHash    string
	baseFee      string
	callErr      error
}

func (f fakeChain) SendRawTransaction(_ context.Context, raw string) (string, error) {
	if f.sentRaw != nil {
		*f.sentRaw = raw
	}
	return f.txHash, f.sendErr
}

func (f fakeChain) TransactionReceipt(_ context.Context, txHash string) (*ethereum.Receipt, error) {
	if f.receipts != nil {
		return f.receipts[txHash], nil
	}
	return f.receipt, nil
}

func (f fakeChain) BlockNumber(context.Context) (uint64, error) {
	return f.block, nil
}

func (f fakeChain) BlockByNumber(context.Context, *uint64) (*ethereum.Block, error) {
	baseFee := f.baseFee
	if baseFee == "" {
		baseFee = "1000000000"
	}
	blockHash := f.blockHash
	if blockHash == "" {
		blockHash = "0x" + strings.Repeat("aa", 32)
	}
	return &ethereum.Block{Hash: blockHash, Number: f.block, BaseFee: baseFee}, nil
}

func (f fakeChain) TransactionByHash(_ context.Context, txHash string) (*ethereum.ChainTransaction, error) {
	if f.transactions != nil {
		return f.transactions[txHash], nil
	}
	return nil, nil
}

func testConfig() Config {
	return Config{
		SignerAddress: "0x00000000000000000000000000000000000000b2",
		ExpiryMargin:  time.Minute,
		MerchantQuota: 100, GlobalQuota: 10_000, QuotaWindow: 24 * time.Hour,
		SigningTimeout:    5 * time.Second,
		LeaseDuration:     2 * time.Minute,
		WorkerInterval:    time.Second,
		Confirmations:     12,
		GasLimit:          100000,
		MaxFeePerGas:      "30000000000",
		MaxPriorityFeeGas: "2000000000",
		RecoveryGrace:     2 * time.Minute,
		ReplacementAfter:  5 * time.Minute,
		// Tiny so tests exercise the wait's timeout fallback without stalling;
		// wait-specific tests override it via the chain fake's receipts.
		ResponseWait: 50 * time.Millisecond,
	}
}

func pendingWork() Work {
	return Work{
		Lease:             Lease{PaymentID: "payment-1", PaymentIdentity: strings.Repeat("ab", 34)},
		TransactionID:     "tx-1",
		TransactionStatus: "intent",
		Nonce:             3,
		Authorization: Authorization{
			From:        "0x1111111111111111111111111111111111111111",
			To:          "0x2222222222222222222222222222222222222222",
			Value:       "1000000",
			ValidAfter:  time.Now().Add(-time.Hour),
			ValidBefore: time.Now().Add(time.Hour),
			Nonce:       "0x" + strings.Repeat("ab", 32),
			Signature:   "0x" + strings.Repeat("11", 32) + strings.Repeat("22", 32) + "1b",
		},
	}
}

func newTestService(store *fakeStore, transactionSigner signer.Signer, chain Chain) *Service {
	return NewService(store, transactionSigner, chain, testConfig(), nil)
}

func TestBroadcastHappyPath(t *testing.T) {
	raw := []byte("raw-tx")
	store := &fakeStore{work: pendingWork()}
	chain := fakeChain{txHash: "0x" + keccakHex(raw)}
	service := newTestService(store, fakeSigner{raw: raw}, chain)
	hash, err := service.Broadcast(context.Background(), "payment-1", "test")
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	if hash != chain.txHash {
		t.Fatalf("hash = %q", hash)
	}
	if len(store.signedRawHash) != 64 {
		t.Fatalf("raw hash = %q", store.signedRawHash)
	}
	if len(store.signedSighash) != 64 {
		t.Fatalf("sighash = %q", store.signedSighash)
	}
	if store.broadcastTxHash != chain.txHash {
		t.Fatalf("recorded hash = %q", store.broadcastTxHash)
	}
	if store.released != 1 {
		t.Fatalf("releases = %d", store.released)
	}
}

func TestBroadcastIdempotentWhenHashRecorded(t *testing.T) {
	work := pendingWork()
	work.TxHash = "0x" + strings.Repeat("ee", 32)
	work.TransactionStatus = "broadcast"
	store := &fakeStore{work: work}
	service := newTestService(store, fakeSigner{err: errors.New("must not sign")}, fakeChain{})
	hash, err := service.Broadcast(context.Background(), "payment-1", "test")
	if err != nil || hash != work.TxHash {
		t.Fatalf("got %q, %v", hash, err)
	}
}

func TestBroadcastLeaseHeld(t *testing.T) {
	store := &fakeStore{claimErr: ErrLeaseUnavailable}
	service := newTestService(store, fakeSigner{}, fakeChain{})
	if _, err := service.Broadcast(context.Background(), "payment-1", "test"); !errors.Is(err, ErrBroadcastPending) {
		t.Fatalf("err = %v, want ErrBroadcastPending", err)
	}
}

func TestBroadcastSigningErrorKeepsIntent(t *testing.T) {
	store := &fakeStore{work: pendingWork()}
	service := newTestService(store, fakeSigner{err: errors.New("kms timeout")}, fakeChain{})
	if _, err := service.Broadcast(context.Background(), "payment-1", "test"); err == nil {
		t.Fatal("expected signing error")
	}
	// The intent is untouched: no signed record, no ambiguity, no expiry.
	if store.signedRawHash != "" || store.ambiguous || store.expired {
		t.Fatalf("store = %+v", store)
	}
}

func TestBroadcastSendErrorMarksAmbiguous(t *testing.T) {
	store := &fakeStore{work: pendingWork()}
	chain := fakeChain{sendErr: errors.New("connection reset")}
	service := newTestService(store, fakeSigner{raw: []byte("raw-tx")}, chain)
	if _, err := service.Broadcast(context.Background(), "payment-1", "test"); err == nil {
		t.Fatal("expected broadcast error")
	}
	if !store.ambiguous {
		t.Fatal("transaction was not marked ambiguous")
	}
	if store.broadcastTxHash != "" {
		t.Fatal("a hash was recorded for an ambiguous broadcast")
	}
}

func TestBroadcastHashMismatchMarksAmbiguous(t *testing.T) {
	store := &fakeStore{work: pendingWork()}
	chain := fakeChain{txHash: "0x" + strings.Repeat("ff", 32)}
	service := newTestService(store, fakeSigner{raw: []byte("raw-tx")}, chain)
	hash, err := service.Broadcast(context.Background(), "payment-1", "test")
	if err == nil || !strings.Contains(err.Error(), "provider returned transaction hash") {
		t.Fatalf("hash = %q, err = %v", hash, err)
	}
	if !store.ambiguous {
		t.Fatal("mismatched acknowledgement was not left for local-hash reconciliation")
	}
	if store.broadcastTxHash != "" {
		t.Fatalf("provider-controlled hash was recorded: %q", store.broadcastTxHash)
	}
}

func TestBroadcastExpiredIntent(t *testing.T) {
	work := pendingWork()
	work.Authorization.ValidBefore = time.Now().Add(30 * time.Second) // inside the 60s margin
	store := &fakeStore{work: work}
	service := newTestService(store, fakeSigner{raw: []byte("raw-tx")}, fakeChain{})
	if _, err := service.Broadcast(context.Background(), "payment-1", "test"); !errors.Is(err, ErrAuthorizationExpiring) {
		t.Fatalf("err = %v, want ErrAuthorizationExpiring", err)
	}
	if !store.expired {
		t.Fatal("intent was not retired as expired")
	}
	if store.signedRawHash != "" {
		t.Fatal("an expiring authorization was signed")
	}
}

func TestBroadcastAmbiguousCrashWindow(t *testing.T) {
	work := pendingWork()
	work.TransactionStatus = "broadcasting" // signed, never recorded a hash
	store := &fakeStore{work: work}
	service := newTestService(store, fakeSigner{err: errors.New("must not re-sign")}, fakeChain{})
	if _, err := service.Broadcast(context.Background(), "payment-1", "test"); err == nil {
		t.Fatal("expected ambiguous broadcast error")
	}
	if !store.ambiguous {
		t.Fatal("stuck broadcasting transaction was not marked ambiguous")
	}
}

func TestConfirmationDepthGating(t *testing.T) {
	work := pendingWork()
	work.TransactionStatus = "broadcast"
	work.TxHash = "0x" + strings.Repeat("ff", 32)
	receipt := &ethereum.Receipt{Status: 1, BlockNumber: 100, BlockHash: "0x" + strings.Repeat("aa", 32), GasUsed: 64336, EffectiveGasPrice: "1000000000"}

	store := &fakeStore{work: work}
	service := newTestService(store, fakeSigner{}, fakeChain{receipt: receipt, block: 105})
	if err := service.Confirmation(context.Background(), "payment-1", "test"); err != nil {
		t.Fatalf("confirmation: %v", err)
	}
	if !store.confirming || store.confirmed {
		t.Fatalf("depth 6 of 12: confirming=%v confirmed=%v", store.confirming, store.confirmed)
	}

	store = &fakeStore{work: work}
	service = newTestService(store, fakeSigner{}, fakeChain{receipt: receipt, block: 111})
	if err := service.Confirmation(context.Background(), "payment-1", "test"); err != nil {
		t.Fatalf("confirmation: %v", err)
	}
	if store.confirming || !store.confirmed {
		t.Fatalf("depth 12 of 12: confirming=%v confirmed=%v", store.confirming, store.confirmed)
	}
}

func TestConfirmationReverted(t *testing.T) {
	work := pendingWork()
	work.TransactionStatus = "broadcast"
	work.TxHash = "0x" + strings.Repeat("ff", 32)
	receipt := &ethereum.Receipt{Status: 0, BlockNumber: 100, BlockHash: "0x" + strings.Repeat("aa", 32), GasUsed: 64336, EffectiveGasPrice: "1000000000"}
	store := &fakeStore{work: work}
	service := newTestService(store, fakeSigner{}, fakeChain{receipt: receipt, block: 120})
	if err := service.Confirmation(context.Background(), "payment-1", "test"); err != nil {
		t.Fatalf("confirmation: %v", err)
	}
	if !store.reverted || store.confirmed {
		t.Fatalf("reverted=%v confirmed=%v", store.reverted, store.confirmed)
	}
}

func TestConfirmationRevertWaitsForFinality(t *testing.T) {
	work := pendingWork()
	work.TransactionStatus = "broadcast"
	work.TxHash = "0x" + strings.Repeat("ff", 32)
	receipt := &ethereum.Receipt{Status: 0, BlockNumber: 100, BlockHash: "0x" + strings.Repeat("aa", 32)}
	store := &fakeStore{work: work}
	service := newTestService(store, fakeSigner{}, fakeChain{receipt: receipt, block: 105})

	if err := service.Confirmation(context.Background(), "payment-1", "test"); err != nil {
		t.Fatalf("confirmation: %v", err)
	}
	if !store.confirming || store.reverted {
		t.Fatalf("confirming=%v reverted=%v", store.confirming, store.reverted)
	}
}

func TestConfirmationRejectsNonCanonicalReceipt(t *testing.T) {
	work := pendingWork()
	work.TransactionStatus = "confirming"
	work.TxHash = "0x" + strings.Repeat("ff", 32)
	receipt := &ethereum.Receipt{Status: 1, BlockNumber: 100, BlockHash: "0x" + strings.Repeat("bb", 32)}
	store := &fakeStore{work: work}
	service := newTestService(store, fakeSigner{}, fakeChain{
		receipt: receipt, block: 111, blockHash: "0x" + strings.Repeat("aa", 32),
	})

	if err := service.Confirmation(context.Background(), "payment-1", "test"); err != nil {
		t.Fatalf("confirmation: %v", err)
	}
	if !store.reorgedOut || store.confirmed || store.reverted {
		t.Fatalf("reorged=%v confirmed=%v reverted=%v", store.reorgedOut, store.confirmed, store.reverted)
	}
}

func TestConfirmationPendingReceipt(t *testing.T) {
	work := pendingWork()
	work.TransactionStatus = "broadcast"
	work.TxHash = "0x" + strings.Repeat("ff", 32)
	store := &fakeStore{work: work}
	service := newTestService(store, fakeSigner{}, fakeChain{receipt: nil, block: 120})
	if err := service.Confirmation(context.Background(), "payment-1", "test"); err != nil {
		t.Fatalf("confirmation: %v", err)
	}
	if store.confirming || store.confirmed || store.reverted {
		t.Fatal("a pending receipt changed state")
	}
}

func TestBroadcastWorkerAmbiguousResume(t *testing.T) {
	work := pendingWork()
	work.TransactionStatus = "broadcasting"
	store := &fakeStore{
		work:   work,
		leases: []Lease{{PaymentID: "payment-1", PaymentIdentity: work.PaymentIdentity, State: StateBroadcasting}},
	}
	service := newTestService(store, fakeSigner{err: errors.New("must not re-sign")}, fakeChain{})
	service.BroadcastWorker().process(context.Background())
	if !store.ambiguous {
		t.Fatal("stuck broadcast was not marked ambiguous")
	}
	if store.released != 1 {
		t.Fatalf("releases = %d", store.released)
	}
}

func TestWorkerReleasesLostLeaseWithoutFailing(t *testing.T) {
	work := pendingWork()
	store := &fakeStore{
		work:       work,
		releaseErr: ErrLeaseLost,
		leases:     []Lease{{PaymentID: "payment-1", PaymentIdentity: work.PaymentIdentity, State: StateBroadcasting}},
	}
	chain := fakeChain{txHash: "0x" + strings.Repeat("ff", 32)}
	service := newTestService(store, fakeSigner{raw: []byte("raw-tx")}, chain)
	// Must not panic or wedge: a lost lease means another worker owns the
	// payment now, and this worker's only job is to stop touching it.
	service.BroadcastWorker().process(context.Background())
	if store.released != 1 {
		t.Fatalf("releases = %d", store.released)
	}
}

func TestWorkerSkipsPaymentWhenBatchLeaseWasLost(t *testing.T) {
	work := pendingWork()
	store := &fakeStore{
		work: work, renewErr: ErrLeaseLost,
		leases: []Lease{{PaymentID: "payment-1", PaymentIdentity: work.PaymentIdentity, State: StateBroadcasting}},
	}
	service := newTestService(store, fakeSigner{err: errors.New("must not sign")}, fakeChain{})
	service.BroadcastWorker().process(context.Background())
	if store.signedRawHash != "" || store.released != 0 {
		t.Fatalf("lost lease was acted on: %+v", store)
	}
}

func TestSettleMapsAdmissionRejections(t *testing.T) {
	cases := map[string]struct {
		err    error
		reason string
	}{
		"not found":    {ErrPaymentNotFound, WireReasonPaymentNotFound},
		"not verified": {ErrPaymentNotVerified, WireReasonPaymentNotVerified},
		"not merchant": {ErrRecipientNotMerchant, WireReasonRecipientNotMerchant},
		"expiring":     {ErrAuthorizationExpiring, WireReasonAuthorizationExpiring},
		"merchant quota": {
			ErrMerchantQuotaExceeded,
			WireReasonMerchantQuotaExceeded,
		},
		"facilitator quota": {
			ErrGlobalQuotaExceeded,
			WireReasonGlobalQuotaExceeded,
		},
	}
	for name, candidate := range cases {
		t.Run(name, func(t *testing.T) {
			store := &fakeStore{intentErr: candidate.err}
			service := newTestService(store, fakeSigner{}, fakeChain{})
			response, err := service.Settle(context.Background(), settleRequest())
			if err != nil {
				t.Fatalf("settle: %v", err)
			}
			if response.Success || response.ErrorReason != candidate.reason {
				t.Fatalf("response = %+v", response)
			}
		})
	}
}

func TestSettleInvalidRequest(t *testing.T) {
	store := &fakeStore{}
	service := newTestService(store, fakeSigner{}, fakeChain{})
	response, err := service.Settle(context.Background(), SettleRequest{})
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	// Parse-layer rejections map to the closed enum's invalid_request; the
	// specific verification reason is diagnostic only, in errorMessage.
	if response.Success || response.ErrorReason != WireReasonInvalidRequest {
		t.Fatalf("response = %+v", response)
	}
	if !strings.Contains(response.ErrorMessage, "invalid_") {
		t.Fatalf("errorMessage = %q, want the specific parse reason", response.ErrorMessage)
	}
}

func TestSettleBroadcastsInline(t *testing.T) {
	raw := []byte("raw-tx")
	work := pendingWork()
	store := &fakeStore{
		work:   work,
		intent: Intent{PaymentID: work.PaymentID, PaymentIdentity: work.PaymentIdentity, TransactionID: work.TransactionID},
	}
	blockHash := "0x" + strings.Repeat("aa", 32)
	chain := fakeChain{
		txHash: "0x" + keccakHex(raw), block: 111, blockHash: blockHash,
		receipt: &ethereum.Receipt{Status: 1, BlockNumber: 100, BlockHash: blockHash},
	}
	service := newTestService(store, fakeSigner{raw: raw}, chain)
	response, err := service.Settle(context.Background(), settleRequest())
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if !response.Success || response.Transaction != chain.txHash {
		t.Fatalf("response = %+v", response)
	}
	if response.Network != "eip155:1" || response.Amount != "1000000" {
		t.Fatalf("response = %+v", response)
	}
}

func TestSettleReturnsTerminalHashIdempotently(t *testing.T) {
	txHash := "0x" + strings.Repeat("dd", 32)
	store := &fakeStore{intent: Intent{
		PaymentID: "payment-1", TransactionID: "tx-1",
		TxHash: txHash, Duplicate: true, Confirmed: true,
	}}
	service := newTestService(store, fakeSigner{err: errors.New("must not sign")}, fakeChain{
		sendErr: errors.New("must not broadcast"),
	})
	response, err := service.Settle(context.Background(), settleRequest())
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if !response.Success || response.Transaction != txHash {
		t.Fatalf("response = %+v", response)
	}
}

func TestSettleReportsConfirmedTransaction(t *testing.T) {
	raw := []byte("raw-tx")
	txHash := "0x" + keccakHex(raw)
	work := pendingWork()
	store := &fakeStore{
		work:   work,
		intent: Intent{PaymentID: work.PaymentID, PaymentIdentity: work.PaymentIdentity, TransactionID: work.TransactionID},
	}
	// Receipt at exactly the required depth: block 111 - 100 + 1 = 12. The
	// fake's BlockByNumber ignores its argument, so the canonical hash is the
	// one the receipt carries.
	blockHash := "0x" + strings.Repeat("cc", 32)
	chain := fakeChain{
		txHash: txHash, block: 111, blockHash: blockHash,
		receipt: &ethereum.Receipt{Status: 1, BlockNumber: 100, BlockHash: blockHash},
	}
	service := newTestService(store, fakeSigner{raw: raw}, chain)
	start := time.Now()
	response, err := service.Settle(context.Background(), settleRequest())
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if !response.Success || response.Transaction != txHash {
		t.Fatalf("response = %+v", response)
	}
	if elapsed := time.Since(start); elapsed > testConfig().ResponseWait {
		t.Fatalf("confirmed transaction took %v; the wait should return immediately", elapsed)
	}
}

func TestSettleReportsFinalizedRevertedTransaction(t *testing.T) {
	raw := []byte("raw-tx")
	txHash := "0x" + keccakHex(raw)
	work := pendingWork()
	store := &fakeStore{
		work:   work,
		intent: Intent{PaymentID: work.PaymentID, PaymentIdentity: work.PaymentIdentity, TransactionID: work.TransactionID},
	}
	// A reverted receipt at exactly the configured depth is a final failure.
	blockHash := "0x" + strings.Repeat("bb", 32)
	chain := fakeChain{
		txHash: txHash, block: 111, blockHash: blockHash,
		receipt: &ethereum.Receipt{Status: 0, BlockNumber: 100, BlockHash: blockHash},
	}
	service := newTestService(store, fakeSigner{raw: raw}, chain)
	start := time.Now()
	response, err := service.Settle(context.Background(), settleRequest())
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if response.Success || response.ErrorReason != WireReasonTransactionReverted {
		t.Fatalf("response = %+v", response)
	}
	if response.Transaction != txHash {
		t.Fatalf("response = %+v, want the reverted hash", response)
	}
	if elapsed := time.Since(start); elapsed > testConfig().ResponseWait {
		t.Fatalf("finalized reverted transaction took %v; response should be immediate", elapsed)
	}
}

func TestSettleReportsConfirmationTimeout(t *testing.T) {
	raw := []byte("raw-tx")
	txHash := "0x" + keccakHex(raw)
	work := pendingWork()
	store := &fakeStore{
		work:   work,
		intent: Intent{PaymentID: work.PaymentID, PaymentIdentity: work.PaymentIdentity, TransactionID: work.TransactionID},
	}
	// No receipt at all: the wait outlives ResponseWait and must not claim the
	// payment settled. The durable hash lets the caller retry idempotently.
	service := newTestService(store, fakeSigner{raw: raw}, fakeChain{txHash: txHash})
	start := time.Now()
	response, err := service.Settle(context.Background(), settleRequest())
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if response.Success || response.ErrorReason != WireReasonConfirmationTimedOut || response.Transaction != txHash {
		t.Fatalf("response = %+v", response)
	}
	if elapsed := time.Since(start); elapsed < testConfig().ResponseWait {
		t.Fatalf("unmined transaction answered in %v, before the wait elapsed", elapsed)
	}
}

func TestSettleDoesNotFinalizeShallowRevert(t *testing.T) {
	raw := []byte("raw-tx")
	txHash := "0x" + keccakHex(raw)
	work := pendingWork()
	store := &fakeStore{
		work:   work,
		intent: Intent{PaymentID: work.PaymentID, PaymentIdentity: work.PaymentIdentity, TransactionID: work.TransactionID},
	}
	blockHash := "0x" + strings.Repeat("bb", 32)
	chain := fakeChain{
		txHash: txHash, block: 100, blockHash: blockHash,
		receipt: &ethereum.Receipt{Status: 0, BlockNumber: 100, BlockHash: blockHash},
	}
	service := newTestService(store, fakeSigner{raw: raw}, chain)
	response, err := service.Settle(context.Background(), settleRequest())
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if response.Success || response.ErrorReason != WireReasonConfirmationTimedOut || response.Transaction != txHash {
		t.Fatalf("response = %+v", response)
	}
}

func TestSettleDuplicateRevertedReturnsFailure(t *testing.T) {
	txHash := "0x" + strings.Repeat("ee", 32)
	store := &fakeStore{intent: Intent{
		PaymentID: "payment-1", TransactionID: "tx-1",
		TxHash: txHash, Duplicate: true, Reverted: true,
	}}
	service := newTestService(store, fakeSigner{err: errors.New("must not sign")}, fakeChain{
		sendErr: errors.New("must not broadcast"),
	})
	response, err := service.Settle(context.Background(), settleRequest())
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if response.Success || response.ErrorReason != WireReasonTransactionReverted {
		t.Fatalf("response = %+v", response)
	}
	if response.Transaction != txHash {
		t.Fatalf("response = %+v, want the reverted hash", response)
	}
}

// settleRequest builds a request that passes the strict verification parser.
// The signature only needs the right shape here: ParseRequest validates
// format, while signature validity was established by /verify before any
// payment record exists.
func settleRequest() SettleRequest {
	requirements := types.PaymentRequirements{
		Scheme: "exact", Network: config.MainnetNetwork, Asset: config.MainnetUSDC,
		Amount: "1000000", PayTo: "0x2222222222222222222222222222222222222222",
		MaxTimeoutSeconds: 60,
		Extra:             map[string]interface{}{"name": "USD Coin", "version": "2"},
	}
	return SettleRequest{
		X402Version: 2, PaymentRequirements: requirements,
		PaymentPayload: types.PaymentPayload{
			X402Version: 2, Accepted: requirements,
			Payload: map[string]interface{}{
				"signature": "0x" + strings.Repeat("11", 32) + strings.Repeat("22", 32) + "1b",
				"authorization": map[string]interface{}{
					"from": "0x1111111111111111111111111111111111111111",
					"to":   requirements.PayTo, "value": "1000000",
					"validAfter": "0", "validBefore": "9999999999",
					"nonce": "0x" + strings.Repeat("ab", 32),
				},
			},
		},
	}
}

func (f fakeChain) Call(context.Context, string, string, []byte) error {
	return f.callErr
}

// A simulation revert retires the intent without broadcasting. Verification
// checked authorizationState, but a nonce consumed between /verify and /settle
// would otherwise be discovered by spending gas on a certain revert — and the
// caller would receive a hash for a doomed transaction.
func TestBroadcastSimulationRevertRetiresIntent(t *testing.T) {
	store := &fakeStore{work: pendingWork()}
	chain := fakeChain{callErr: fmt.Errorf("%w: nonce already used", ethereum.ErrSimulationReverted)}
	service := newTestService(store, fakeSigner{raw: []byte("raw-tx")}, chain)
	if _, err := service.Broadcast(context.Background(), "payment-1", "test"); !errors.Is(err, ErrPaymentUnsettleable) {
		t.Fatalf("err = %v, want ErrPaymentUnsettleable", err)
	}
	if store.unsettleable != 1 {
		t.Fatalf("unsettleable markings = %d, want 1", store.unsettleable)
	}
	// Nothing signed, nothing sent, no ambiguity to reconcile.
	if store.signedRawHash != "" {
		t.Fatal("a payment that cannot settle was signed")
	}
	if store.broadcastTxHash != "" || store.ambiguous {
		t.Fatalf("store = %+v", store)
	}
}

// A simulation that could not run is transient: the committed intent must be left
// for the next tick rather than abandoning a payment that may be perfectly fine.
func TestBroadcastSimulationFailureKeepsIntent(t *testing.T) {
	store := &fakeStore{work: pendingWork()}
	chain := fakeChain{callErr: errors.New("limit exceeded")}
	service := newTestService(store, fakeSigner{raw: []byte("raw-tx")}, chain)
	_, err := service.Broadcast(context.Background(), "payment-1", "test")
	if err == nil {
		t.Fatal("transient simulation failure was ignored")
	}
	if errors.Is(err, ErrPaymentUnsettleable) {
		t.Fatal("a transient simulation failure must not retire the payment")
	}
	if store.unsettleable != 0 {
		t.Fatalf("unsettleable markings = %d, want 0", store.unsettleable)
	}
	if store.signedRawHash != "" || store.broadcastTxHash != "" {
		t.Fatalf("store = %+v", store)
	}
}

// TestBroadcastRecordsItsHashDespiteCancellation is the property that makes a
// deploy safe. Shutdown cancels the worker's context; if that cancellation landed
// between sending the transaction and recording its hash, the transaction would be
// on the network with nothing recording it — the ambiguous case, which costs a
// human to resolve. Every restart was previously a chance to create one.
func TestBroadcastRecordsItsHashDespiteCancellation(t *testing.T) {
	raw := []byte("raw-tx")
	store := &fakeStore{work: pendingWork()}
	chain := &cancellationAwareChain{txHash: "0x" + keccakHex(raw)}
	service := newTestService(store, fakeSigner{raw: raw}, chain)

	// Cancelled before the pipeline runs: the harshest version of a shutdown
	// arriving mid-broadcast.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	work := pendingWork()
	work.Lease = Lease{PaymentID: "payment-1", Worker: "test"}
	hash, err := service.broadcastClaimed(ctx, work, "worker")
	if err != nil {
		t.Fatalf("broadcast under cancellation: %v", err)
	}
	if hash != chain.txHash {
		t.Fatalf("hash = %q, want %q", hash, chain.txHash)
	}
	if store.broadcastTxHash != chain.txHash {
		t.Fatalf("the hash was not recorded (got %q); the transaction would be orphaned",
			store.broadcastTxHash)
	}
	if store.ambiguous {
		t.Fatal("a successful broadcast was marked ambiguous")
	}
	if !chain.sawLiveContext {
		t.Fatal("the send received an already-cancelled context; it would never reach the network")
	}
}

// cancellationAwareChain reports whether the context it was handed was still live,
// which is what distinguishes a detached broadcast from a doomed one.
type cancellationAwareChain struct {
	fakeChain
	txHash         string
	sawLiveContext bool
}

func (c *cancellationAwareChain) SendRawTransaction(ctx context.Context, _ string) (string, error) {
	if ctx.Err() == nil {
		c.sawLiveContext = true
	}
	return c.txHash, ctx.Err()
}
