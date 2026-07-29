package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/config"
	"github.com/ETH402/facilitator/internal/ethereum"
	"github.com/ETH402/facilitator/internal/metrics"
	"github.com/ETH402/facilitator/internal/settlement"
	"github.com/ETH402/facilitator/internal/signer"
	"github.com/ETH402/facilitator/internal/stats"
	x402 "github.com/x402-foundation/x402/go/v2"
	"github.com/x402-foundation/x402/go/v2/types"
)

type settleFakeStore struct{ work settlement.Work }

func (f settleFakeStore) CreateSettlementIntent(_ context.Context, request settlement.IntentRequest) (settlement.Intent, error) {
	return settlement.Intent{
		PaymentID: f.work.PaymentID, PaymentIdentity: request.PaymentIdentity,
		TransactionID: f.work.TransactionID, Nonce: f.work.Nonce,
	}, nil
}

func (f settleFakeStore) ClaimPayment(_ context.Context, paymentID, worker string, duration time.Duration, now time.Time) (settlement.Lease, error) {
	return settlement.Lease{PaymentID: paymentID, Worker: worker, Until: now.Add(duration)}, nil
}

func (f settleFakeStore) ClaimPayments(context.Context, settlement.ClaimRequest) ([]settlement.Lease, error) {
	return nil, nil
}

func (f settleFakeStore) ReleaseLease(context.Context, string, string) error { return nil }

func (f settleFakeStore) LoadSettlementWork(context.Context, string) (settlement.Work, error) {
	return f.work, nil
}

func (f settleFakeStore) MarkTxSigned(context.Context, string, string, uint64, string, string) error {
	return nil
}

func (f settleFakeStore) MarkTxBroadcast(context.Context, string, string, string, string) error {
	return nil
}

func (f settleFakeStore) MarkTxAmbiguous(context.Context, string, string, string) error { return nil }

func (f settleFakeStore) MarkIntentExpired(context.Context, string, string, string) error { return nil }

func (f settleFakeStore) MarkTxConfirming(context.Context, string, string, uint64, string, string) error {
	return nil
}

func (f settleFakeStore) MarkTxConfirmed(context.Context, string, string, uint64, string, uint64, string, string) error {
	return nil
}

func (f settleFakeStore) MarkTxReverted(context.Context, string, string, uint64, string, string) error {
	return nil
}

func (f settleFakeStore) MarkTxRecoveredBroadcast(context.Context, string, string, string, string) error {
	return nil
}

func (f settleFakeStore) MarkTxReplaced(context.Context, string, string, settlement.Replacement, string) error {
	return nil
}

func (f settleFakeStore) MarkReplacementLanded(context.Context, string, string, bool, uint64, string, uint64, string, string) error {
	return nil
}

func (f settleFakeStore) MarkTxReorgedOut(context.Context, string, string, string) error {
	return nil
}

func (f settleFakeStore) ListReplacedPending(context.Context) ([]settlement.TrackedTransaction, error) {
	return nil, nil
}

func (f settleFakeStore) ListDroppedBlockingGaps(context.Context, string) ([]settlement.Work, error) {
	return nil, nil
}

func (f settleFakeStore) ListGapFillers(context.Context) ([]settlement.TrackedTransaction, error) {
	return nil, nil
}

func (f settleFakeStore) MarkGapFillerBroadcast(context.Context, string, string, string, uint64, string, string) error {
	return nil
}

func (f settleFakeStore) MarkGapFillerResolved(context.Context, string, uint64, string) error {
	return nil
}

type settleFakeSigner struct{}

func (settleFakeSigner) Address(context.Context) (string, error) {
	return "0x00000000000000000000000000000000000000b2", nil
}

func (settleFakeSigner) SignTransaction(_ context.Context, tx signer.Transaction) (signer.SignedTransaction, error) {
	if err := tx.Validate(); err != nil {
		return signer.SignedTransaction{}, err
	}
	return signer.SignedTransaction{Raw: []byte("signed-raw-transaction")}, nil
}

type settleFakeChain struct{ txHash string }

func (f settleFakeChain) SendRawTransaction(context.Context, string) (string, error) {
	return f.txHash, nil
}

func (f settleFakeChain) TransactionReceipt(context.Context, string) (*ethereum.Receipt, error) {
	return nil, nil
}

func (f settleFakeChain) BlockNumber(context.Context) (uint64, error) { return 0, nil }

func (f settleFakeChain) BlockByNumber(context.Context, *uint64) (*ethereum.Block, error) {
	return &ethereum.Block{BaseFee: "1000000000"}, nil
}

func (f settleFakeChain) TransactionByHash(context.Context, string) (*ethereum.ChainTransaction, error) {
	return nil, nil
}

func settleTestService(txHash string) *settlement.Service {
	work := settlement.Work{
		Lease:             settlement.Lease{PaymentID: "payment-1", PaymentIdentity: strings.Repeat("ab", 34)},
		TransactionID:     "tx-1",
		TransactionStatus: "intent",
		Nonce:             0,
		Authorization: settlement.Authorization{
			From:  "0x2222222222222222222222222222222222222222",
			To:    "0x1111111111111111111111111111111111111111",
			Value: "1", ValidAfter: time.Now().Add(-time.Hour),
			ValidBefore: time.Now().Add(time.Hour),
			Nonce:       "0x" + strings.Repeat("33", 32),
			Signature:   "0x" + strings.Repeat("11", 32) + strings.Repeat("22", 32) + "1b",
		},
	}
	return settlement.NewService(settleFakeStore{work: work}, settleFakeSigner{}, settleFakeChain{txHash: txHash}, settlement.Config{
		SignerAddress: "0x00000000000000000000000000000000000000b2",
		ExpiryMargin:  time.Minute, SigningTimeout: 5 * time.Second,
		MerchantQuota: 100, QuotaWindow: 24 * time.Hour,
		LeaseDuration: 2 * time.Minute, WorkerInterval: time.Second, Confirmations: 12,
		GasLimit: 120000, MaxFeePerGas: "30000000000", MaxPriorityFeeGas: "2000000000",
	}, nil)
}

func settleTestBody(t *testing.T) string {
	t.Helper()
	requirements := types.PaymentRequirements{
		Scheme: "exact", Network: config.MainnetNetwork, Asset: config.MainnetUSDC,
		Amount: "1", PayTo: "0x1111111111111111111111111111111111111111",
		MaxTimeoutSeconds: 60,
		Extra: map[string]interface{}{
			"name": "USD Coin", "version": "2", "assetTransferMethod": "eip3009",
		},
	}
	request := settlement.SettleRequest{
		X402Version: 2, PaymentRequirements: requirements,
		PaymentPayload: types.PaymentPayload{
			X402Version: 2, Accepted: requirements,
			Payload: map[string]interface{}{
				"signature": "0x" + strings.Repeat("11", 32) + strings.Repeat("22", 32) + "1b",
				"authorization": map[string]interface{}{
					"from": "0x2222222222222222222222222222222222222222",
					"to":   requirements.PayTo, "value": "1", "validAfter": "0",
					"validBefore": big.NewInt(time.Now().Add(time.Hour).Unix()).String(),
					"nonce":       "0x" + strings.Repeat("33", 32),
				},
			},
		},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func settleHandler(service *settlement.Service, registry *metrics.Registry) http.Handler {
	return New(Dependencies{
		Logger: slog.Default(), Database: fakeDB{}, Ethereum: fakeRPC{chain: 1},
		Stats: stats.NewService(statsSource{}, time.Now(), 0), Metrics: registry,
		ExpectedChainID: 1, PublicRatePerMinute: 100, Settlement: service,
	}).Handler()
}

func TestSettleEndpoint(t *testing.T) {
	t.Parallel()
	txHash := "0x" + strings.Repeat("ab", 32)
	handler := settleHandler(settleTestService(txHash), metrics.New())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/settle", strings.NewReader(settleTestBody(t))))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response x402.SettleResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Success || response.Transaction != txHash || response.Network != "eip155:1" {
		t.Fatalf("response = %#v", response)
	}
}

func TestSettleUnavailableWithoutSigner(t *testing.T) {
	t.Parallel()
	handler := settleHandler(nil, metrics.New())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/settle", strings.NewReader(settleTestBody(t))))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["errorReason"] != settlement.WireReasonSettlementUnavailable {
		t.Fatalf("response = %v", response)
	}
}

func TestSettleRejectsMalformedBody(t *testing.T) {
	t.Parallel()
	handler := settleHandler(settleTestService("0x"), metrics.New())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodPost, "/settle", strings.NewReader(`{"x402Version":2,"unknown":true}`),
	))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
