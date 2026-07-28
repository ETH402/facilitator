package settlement

import (
	"context"
	"errors"
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
	releaseErr error
	leases     []Lease

	signedRawHash   string
	broadcastTxHash string
	ambiguous       bool
	expired         bool
	confirming      bool
	confirmed       bool
	reverted        bool
	released        int
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

func (f *fakeStore) ReleaseLease(context.Context, string, string) error {
	f.released++
	return f.releaseErr
}

func (f *fakeStore) LoadSettlementWork(context.Context, string) (Work, error) {
	return f.work, nil
}

func (f *fakeStore) MarkTxSigned(_ context.Context, _, rawHash string) error {
	f.signedRawHash = rawHash
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

type fakeSigner struct {
	raw []byte
	err error
}

func (f fakeSigner) Address(context.Context) (string, error) {
	return "0x00000000000000000000000000000000000000b2", nil
}

func (f fakeSigner) SignTransaction(context.Context, signer.Transaction) (signer.SignedTransaction, error) {
	return signer.SignedTransaction{Raw: f.raw}, f.err
}

type fakeChain struct {
	txHash  string
	sendErr error
	receipt *ethereum.Receipt
	block   uint64
}

func (f fakeChain) SendRawTransaction(context.Context, string) (string, error) {
	return f.txHash, f.sendErr
}

func (f fakeChain) TransactionReceipt(context.Context, string) (*ethereum.Receipt, error) {
	return f.receipt, nil
}

func (f fakeChain) BlockNumber(context.Context) (uint64, error) {
	return f.block, nil
}

func testConfig() Config {
	return Config{
		SignerAddress:     "0x00000000000000000000000000000000000000b2",
		ExpiryMargin:      time.Minute,
		SigningTimeout:    5 * time.Second,
		LeaseDuration:     2 * time.Minute,
		WorkerInterval:    time.Second,
		Confirmations:     12,
		GasLimit:          100000,
		MaxFeePerGas:      "30000000000",
		MaxPriorityFeeGas: "2000000000",
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
	store := &fakeStore{work: pendingWork()}
	chain := fakeChain{txHash: "0x" + strings.Repeat("ff", 32)}
	service := newTestService(store, fakeSigner{raw: []byte("raw-tx")}, chain)
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
	receipt := &ethereum.Receipt{Status: 1, BlockNumber: 100, BlockHash: "0x" + strings.Repeat("cd", 32), GasUsed: 64336, EffectiveGasPrice: "1000000000"}

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
	receipt := &ethereum.Receipt{Status: 0, BlockNumber: 100, GasUsed: 64336, EffectiveGasPrice: "1000000000"}
	store := &fakeStore{work: work}
	service := newTestService(store, fakeSigner{}, fakeChain{receipt: receipt, block: 120})
	if err := service.Confirmation(context.Background(), "payment-1", "test"); err != nil {
		t.Fatalf("confirmation: %v", err)
	}
	if !store.reverted || store.confirmed {
		t.Fatalf("reverted=%v confirmed=%v", store.reverted, store.confirmed)
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

func TestSettleMapsAdmissionRejections(t *testing.T) {
	cases := map[string]struct {
		err    error
		reason string
	}{
		"not found":    {ErrPaymentNotFound, WireReasonPaymentNotFound},
		"not verified": {ErrPaymentNotVerified, WireReasonPaymentNotVerified},
		"not merchant": {ErrRecipientNotMerchant, WireReasonRecipientNotMerchant},
		"expiring":     {ErrAuthorizationExpiring, WireReasonAuthorizationExpiring},
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
	if response.Success || response.ErrorReason == "" {
		t.Fatalf("response = %+v", response)
	}
}

func TestSettleBroadcastsInline(t *testing.T) {
	work := pendingWork()
	store := &fakeStore{
		work:   work,
		intent: Intent{PaymentID: work.PaymentID, PaymentIdentity: work.PaymentIdentity, TransactionID: work.TransactionID},
	}
	chain := fakeChain{txHash: "0x" + strings.Repeat("ff", 32)}
	service := newTestService(store, fakeSigner{raw: []byte("raw-tx")}, chain)
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
