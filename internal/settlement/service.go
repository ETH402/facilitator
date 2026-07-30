package settlement

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/ETH402/facilitator/internal/config"
	"github.com/ETH402/facilitator/internal/ethereum"
	"github.com/ETH402/facilitator/internal/signer"
	"github.com/ETH402/facilitator/internal/verification"
	x402 "github.com/x402-foundation/x402/go/v2"
	"golang.org/x/crypto/sha3"
)

// broadcastGrace bounds the send-and-record pair once it has begun. Long enough
// for an RPC send plus a database write, short enough that a shutdown waiting on it
// still terminates promptly.
const broadcastGrace = 30 * time.Second

// ErrBroadcastPending means the payment is being broadcast by another actor
// (its lease is held) and no transaction hash is recorded yet. Callers should
// retry rather than compete.
var ErrBroadcastPending = errors.New("payment broadcast is already in progress")

// Store is the persistence the settlement flow needs. *store.Store implements
// it; tests use fakes so the pipeline runs without PostgreSQL.
type Store interface {
	CreateSettlementIntent(context.Context, IntentRequest) (Intent, error)
	ClaimPayment(ctx context.Context, paymentID, worker string, duration time.Duration, now time.Time) (Lease, error)
	ClaimPayments(context.Context, ClaimRequest) ([]Lease, error)
	RenewLease(ctx context.Context, paymentID, worker string, now time.Time, duration time.Duration) (time.Time, error)
	ReleaseLease(ctx context.Context, paymentID, worker string) error
	LoadSettlementWork(ctx context.Context, paymentID string) (Work, error)
	MarkTxSigned(ctx context.Context, transactionID, rawHash, sighash string, gasLimit uint64, maxFee, priorityFee string) error
	MarkTxBroadcast(ctx context.Context, paymentID, transactionID, txHash, actor string) error
	MarkTxAmbiguous(ctx context.Context, paymentID, transactionID, actor string) error
	MarkIntentExpired(ctx context.Context, paymentID, transactionID, actor string) error
	MarkIntentUnsettleable(ctx context.Context, paymentID, transactionID, actor string) error
	MarkTxConfirming(ctx context.Context, paymentID, transactionID string, blockNumber uint64, blockHash, actor string) error
	MarkTxConfirmed(ctx context.Context, paymentID, transactionID string, blockNumber uint64, blockHash string, gasUsed uint64, gasPrice, actor string) error
	MarkTxReverted(ctx context.Context, paymentID, transactionID string, gasUsed uint64, gasPrice, actor string) error
	MarkTxRecoveredBroadcast(ctx context.Context, paymentID, transactionID, txHash, actor string) error
	MarkTxAmbiguousReplaced(ctx context.Context, paymentID, oldTxID string, replacement Replacement, actor string) error
	MarkTxReplaced(ctx context.Context, paymentID, oldTxID string, replacement Replacement, actor string) error
	MarkReplacementLanded(ctx context.Context, paymentID, minedTxID string, succeeded bool, blockNumber uint64, blockHash string, gasUsed uint64, gasPrice, actor string) error
	MarkTxReorgedOut(ctx context.Context, paymentID, transactionID, actor string) error
	ListReplacedPending(ctx context.Context) ([]TrackedTransaction, error)
	ListDroppedBlockingGaps(ctx context.Context, signerAddress string, expiredBefore time.Time) ([]Work, error)
	ListGapFillers(ctx context.Context) ([]TrackedTransaction, error)
	MarkGapFillerPrepared(ctx context.Context, transactionID, rawHash, txHash string, raw []byte, gasLimit uint64, maxFee, priorityFee string) error
	MarkGapFillerResolved(ctx context.Context, transactionID string, gasUsed uint64, gasPrice string) error
	MarkGapFillerSucceeded(ctx context.Context, paymentID, transactionID string, blockNumber uint64, blockHash string, gasUsed uint64, gasPrice, actor string) error
}

// Chain is the Ethereum surface settlement uses. Broadcasting a transaction
// and reading receipts are deliberately separate failure modes (ADR-0004).
type Chain interface {
	ethereum.Broadcaster
	ethereum.ReceiptReader
	BlockNumber(context.Context) (uint64, error)
	BlockByNumber(ctx context.Context, number *uint64) (*ethereum.Block, error)
	TransactionByHash(ctx context.Context, txHash string) (*ethereum.ChainTransaction, error)
	// Call simulates the literal calldata that would be broadcast. A revert is
	// reported as ethereum.ErrSimulationReverted.
	Call(ctx context.Context, from, to string, data []byte) error
}

// Config pins every value the settlement path is allowed to spend or wait.
// MaxFeePerGas is the hard spend ceiling: initial fees are estimated beneath
// it and replacement bumps may never cross it (ADR-0004 decision 6).
type Config struct {
	SignerAddress     string
	ExpiryMargin      time.Duration
	MerchantQuota     int
	GlobalQuota       int
	QuotaWindow       time.Duration
	SigningTimeout    time.Duration
	LeaseDuration     time.Duration
	WorkerInterval    time.Duration
	Confirmations     uint64
	GasLimit          uint64
	MaxFeePerGas      string
	MaxPriorityFeeGas string
	// RecoveryGrace is how long recovery waits after an ambiguous broadcast
	// before re-broadcasting the identical transaction; within the window
	// only on-chain lookups run.
	RecoveryGrace time.Duration
	// ReplacementAfter is how long a broadcast may sit pending before
	// recovery replaces it with a fee bump.
	ReplacementAfter time.Duration
}

// Service runs settlement: admission, the broadcast pipeline shared by HTTP
// and the worker, and confirmation.
type Service struct {
	heartbeat Heartbeater
	store     Store
	signer    signer.Signer
	chain     Chain
	cfg       Config
	logger    *slog.Logger
	now       func() time.Time
}

func NewService(store Store, transactionSigner signer.Signer, chain Chain, cfg Config, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: store, signer: transactionSigner, chain: chain, cfg: cfg, logger: logger, now: time.Now}
}

// Settle admits a payment to settlement and attempts an inline broadcast.
//
// Admission reuses the verification parser, so only a payment /verify would
// have accepted can settle, and requires the prior verification record: the
// durable row is what binds the recipient to a registered merchant (ADR-0004
// decision 9). Policy rejections return a successful wire response carrying
// Success=false and a stable reason; only internal failures return an error.
func (s *Service) Settle(ctx context.Context, request SettleRequest) (*x402.SettleResponse, error) {
	payment, reason := verification.ParseRequest(request)
	if reason != "" {
		// Parse rejections name a payment /verify would have refused. The
		// specific reason is diagnostic, not contract — the OpenAPI enum is
		// closed — so it travels in errorMessage behind invalid_request.
		response := rejected(WireReasonInvalidRequest, payment)
		response.ErrorMessage = "payment is not acceptable to this facilitator: " + reason
		return response, nil
	}
	intent, err := s.store.CreateSettlementIntent(ctx, IntentRequest{
		PaymentIdentity: payment.Identity,
		SignerAddress:   s.cfg.SignerAddress,
		PayerSignature:  payment.Signature,
		ExpiryMargin:    s.cfg.ExpiryMargin,
		Quota:           s.cfg.MerchantQuota,
		GlobalQuota:     s.cfg.GlobalQuota,
		QuotaWindow:     s.cfg.QuotaWindow,
		Now:             s.now(),
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrPaymentNotFound):
			return rejected(WireReasonPaymentNotFound, payment), nil
		case errors.Is(err, ErrPaymentNotVerified):
			return rejected(WireReasonPaymentNotVerified, payment), nil
		case errors.Is(err, ErrRecipientNotMerchant):
			return rejected(WireReasonRecipientNotMerchant, payment), nil
		case errors.Is(err, ErrAuthorizationExpiring):
			return rejected(WireReasonAuthorizationExpiring, payment), nil
		case errors.Is(err, ErrMerchantQuotaExceeded):
			return rejected(WireReasonMerchantQuotaExceeded, payment), nil
		case errors.Is(err, ErrGlobalQuotaExceeded):
			return rejected(WireReasonGlobalQuotaExceeded, payment), nil
		}
		return nil, fmt.Errorf("create settlement intent: %w", err)
	}
	if intent.TxHash != "" {
		return settled(payment, intent.TxHash), nil
	}

	txHash, err := s.Broadcast(ctx, intent.PaymentID, "http")
	if err == nil {
		return settled(payment, txHash), nil
	}
	if errors.Is(err, ErrAuthorizationExpiring) {
		return rejected(WireReasonAuthorizationExpiring, payment), nil
	}
	if errors.Is(err, ErrPaymentUnsettleable) {
		// Simulation proved the transfer cannot succeed, so the caller learns now
		// rather than receiving a hash for a transaction certain to revert.
		return rejected(WireReasonSimulationReverted, payment), nil
	}
	// The broadcast may still have been recorded by a concurrent actor (a
	// worker holding the lease, or a race resolved after our send error).
	work, loadErr := s.store.LoadSettlementWork(ctx, intent.PaymentID)
	if loadErr == nil && work.TxHash != "" {
		return settled(payment, work.TxHash), nil
	}
	if errors.Is(err, ErrBroadcastPending) {
		return &x402.SettleResponse{
			Success: false, ErrorReason: WireReasonBroadcastFailed,
			ErrorMessage: "payment broadcast is already in progress; retry to observe it",
			Payer:        payment.Payer, Network: config.MainnetNetwork, Amount: payment.Amount,
		}, nil
	}
	s.logger.ErrorContext(ctx, "settlement broadcast failed",
		"payment_identity", payment.Identity, "error", err)
	return &x402.SettleResponse{
		Success: false, ErrorReason: WireReasonBroadcastFailed,
		ErrorMessage: "transaction broadcast failed; the durable intent remains for the settlement worker",
		Payer:        payment.Payer, Network: config.MainnetNetwork, Amount: payment.Amount,
	}, nil
}

// estimateFees prices a settlement transaction from the latest block's base
// fee, bounded by the operator's configured ceiling: the ceiling remains the
// hard spend limit while estimation avoids overpaying beneath it and leaves
// headroom for replacement bumps.
func (s *Service) estimateFees(ctx context.Context) (maxFee, priority *big.Int, err error) {
	block, err := s.chain.BlockByNumber(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	baseFee, ok := new(big.Int).SetString(block.BaseFee, 10)
	if !ok {
		return nil, nil, fmt.Errorf("latest block reports invalid base fee %q", block.BaseFee)
	}
	tip, ok := new(big.Int).SetString(s.cfg.MaxPriorityFeeGas, 10)
	if !ok {
		return nil, nil, fmt.Errorf("configured priority fee %q is not a decimal integer", s.cfg.MaxPriorityFeeGas)
	}
	ceiling, ok := new(big.Int).SetString(s.cfg.MaxFeePerGas, 10)
	if !ok {
		return nil, nil, fmt.Errorf("configured max fee %q is not a decimal integer", s.cfg.MaxFeePerGas)
	}
	return EstimateFees(baseFee, tip, ceiling)
}

// Broadcast runs the shared pipeline for one payment: claim the lease, sign
// the committed intent exactly once, broadcast once, record the hash. The
// order is ADR-0004 decision 3 — the intent and nonce are durable before
// anything is signed, so a crash always leaves a record of what was spoken
// for.
func (s *Service) Broadcast(ctx context.Context, paymentID, actor string) (string, error) {
	lease, err := s.store.ClaimPayment(ctx, paymentID, actor, s.cfg.LeaseDuration, s.now())
	if err != nil {
		if errors.Is(err, ErrLeaseUnavailable) {
			return "", ErrBroadcastPending
		}
		return "", fmt.Errorf("claim payment: %w", err)
	}
	defer s.release(ctx, paymentID, actor)
	work, err := s.store.LoadSettlementWork(ctx, paymentID)
	if err != nil {
		return "", fmt.Errorf("load settlement work: %w", err)
	}
	work.Lease = lease
	return s.broadcastClaimed(ctx, work, actor)
}

// broadcastClaimed assumes the caller holds the payment lease (inline settle
// claims it, the worker claimed it in a batch). It is idempotent: a payment
// whose hash is already recorded returns that hash without touching the
// network.
func (s *Service) broadcastClaimed(ctx context.Context, work Work, actor string) (string, error) {
	if work.TxHash != "" {
		return work.TxHash, nil
	}
	if work.AmbiguousBroadcast() {
		// A previous attempt crashed between broadcast and recording the hash.
		// Re-broadcasting would risk a second spend of the authorization; only
		// on-chain reconciliation may resolve this (ADR-0004 decision 4).
		if err := s.store.MarkTxAmbiguous(ctx, work.PaymentID, work.TransactionID, actor); err != nil {
			return "", fmt.Errorf("mark ambiguous broadcast: %w", err)
		}
		return "", errors.New("broadcast outcome is ambiguous; payment moved to manual review")
	}
	if !work.BroadcastPending() {
		return "", fmt.Errorf("transaction %s in unexpected status %q", work.TransactionID, work.TransactionStatus)
	}
	if !work.Authorization.ValidBefore.After(s.now().Add(s.cfg.ExpiryMargin)) {
		// EIP-3009 enforces validBefore on chain, so broadcasting now buys a
		// predictable revert with the operator's gas (ADR-0004 decision 11).
		if err := s.store.MarkIntentExpired(ctx, work.PaymentID, work.TransactionID, actor); err != nil {
			return "", fmt.Errorf("retire expired intent: %w", err)
		}
		return "", ErrAuthorizationExpiring
	}
	wire := work.Authorization.Wire()
	calldata, err := TransferWithAuthorizationData(work.Authorization)
	if err != nil {
		return "", fmt.Errorf("build calldata: %w", err)
	}
	// Simulate before signing. Verification checked authorizationState, but a
	// nonce consumed between /verify and /settle — the conflicting-facilitators
	// race — would otherwise be discovered by spending gas on a certain revert,
	// and the caller would be handed a hash for a doomed transaction. Simulating
	// first also avoids a signer round trip on a payment that cannot settle.
	if err := s.chain.Call(ctx, work.SignerAddress, config.MainnetUSDC, calldata); err != nil {
		if !errors.Is(err, ethereum.ErrSimulationReverted) {
			// Could not simulate: transient, so leave the committed intent for the
			// next tick rather than abandoning a payment that may be fine.
			return "", fmt.Errorf("simulate transfer: %w", err)
		}
		s.logger.WarnContext(ctx, "settlement simulation reverted; retiring intent unbroadcast",
			"payment_id", work.PaymentID, "transaction_id", work.TransactionID, "error", err)
		if markErr := s.store.MarkIntentUnsettleable(ctx, work.PaymentID, work.TransactionID, actor); markErr != nil {
			return "", fmt.Errorf("retire unsettleable intent: %w", markErr)
		}
		return "", ErrPaymentUnsettleable
	}
	maxFee, priorityFee, err := s.estimateFees(ctx)
	if err != nil {
		// Fees are never guessed: a failed estimate leaves the committed
		// intent untouched for the next tick.
		return "", fmt.Errorf("estimate fees: %w", err)
	}
	signCtx, cancel := context.WithTimeout(ctx, s.cfg.SigningTimeout)
	signed, err := s.signer.SignTransaction(signCtx, signer.Transaction{
		ChainID:              config.MainnetChainID,
		Nonce:                work.Nonce,
		To:                   config.MainnetUSDC,
		Data:                 calldata,
		Value:                "0",
		GasLimit:             s.cfg.GasLimit,
		MaxFeePerGas:         maxFee.String(),
		MaxPriorityFeePerGas: priorityFee.String(),
		Authorization:        &wire,
	})
	cancel()
	if err != nil {
		// Nothing was signed, so nothing can be on chain: the intent stays
		// exactly as committed and the next attempt signs the same nonce again.
		return "", fmt.Errorf("sign settlement transaction: %w", err)
	}
	keccak := sha3.NewLegacyKeccak256()
	keccak.Write(signed.Raw)
	rawSum := keccak.Sum(nil)
	if err := s.store.MarkTxSigned(ctx, work.TransactionID, hex.EncodeToString(rawSum),
		hex.EncodeToString(signed.SigHash[:]), s.cfg.GasLimit, maxFee.String(), priorityFee.String()); err != nil {
		return "", fmt.Errorf("record signed transaction: %w", err)
	}
	// The send and the write that records its result are detached from
	// cancellation. A shutdown landing between them leaves a transaction on the
	// network whose hash was never recorded, which is precisely the ambiguous case
	// — and that case costs a human. Since a deploy cancels this context, the
	// unprotected version made every restart a chance to strand a payment in
	// manual_review. Bounded so shutdown still terminates.
	broadcastCtx, endBroadcast := context.WithTimeout(context.WithoutCancel(ctx), broadcastGrace)
	defer endBroadcast()
	txHash, err := s.chain.SendRawTransaction(broadcastCtx, "0x"+hex.EncodeToString(signed.Raw))
	if err != nil {
		// The outcome is unknown: the provider may have accepted the
		// transaction. Mark it ambiguous; recovery resolves it on chain.
		if markErr := s.store.MarkTxAmbiguous(broadcastCtx, work.PaymentID, work.TransactionID, actor); markErr != nil {
			return "", errors.Join(fmt.Errorf("broadcast: %w", err), fmt.Errorf("mark ambiguous: %w", markErr))
		}
		return "", fmt.Errorf("broadcast: %w", err)
	}
	if err := s.store.MarkTxBroadcast(broadcastCtx, work.PaymentID, work.TransactionID, txHash, actor); err != nil {
		return "", fmt.Errorf("record broadcast: %w", err)
	}
	return txHash, nil
}

// Confirmation observes one leased payment's transaction once. It is the
// confirmation worker's unit of work, split out so tests can drive it without
// a ticker.
func (s *Service) Confirmation(ctx context.Context, paymentID, actor string) error {
	work, err := s.store.LoadSettlementWork(ctx, paymentID)
	if err != nil {
		return fmt.Errorf("load settlement work: %w", err)
	}
	if work.TxHash == "" {
		return fmt.Errorf("payment %s in state %s has no transaction hash", paymentID, work.State)
	}
	receipt, err := s.chain.TransactionReceipt(ctx, work.TxHash)
	if err != nil {
		return fmt.Errorf("fetch receipt: %w", err)
	}
	if receipt == nil {
		if work.TransactionStatus == "confirming" {
			// The tx was seen mined but now has no canonical receipt: its
			// block was reorged out. Return it to broadcast so it is
			// observed from scratch. (A lagging provider can look the same;
			// the next tick's receipt re-confirms it, so the thrash is
			// harmless.)
			return s.store.MarkTxReorgedOut(ctx, paymentID, work.TransactionID, actor)
		}
		return nil // Not yet mined; the next tick looks again.
	}
	canonical, err := s.chain.BlockByNumber(ctx, &receipt.BlockNumber)
	if err != nil {
		return fmt.Errorf("fetch canonical receipt block: %w", err)
	}
	if !strings.EqualFold(canonical.Hash, receipt.BlockHash) {
		// A receipt is only evidence of inclusion when its block is still
		// canonical. If this transaction had already been observed, unwind the
		// non-final sighting; otherwise leave it broadcast and try again.
		if work.TransactionStatus == "confirming" {
			return s.store.MarkTxReorgedOut(ctx, paymentID, work.TransactionID, actor)
		}
		return nil
	}
	current, err := s.chain.BlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("fetch block number: %w", err)
	}
	depth := uint64(0)
	if current >= receipt.BlockNumber {
		depth = current - receipt.BlockNumber + 1
	}
	if depth < s.cfg.Confirmations {
		return s.store.MarkTxConfirming(ctx, paymentID, work.TransactionID,
			receipt.BlockNumber, receipt.BlockHash, actor)
	}
	if receipt.Status == 0 {
		return s.store.MarkTxReverted(ctx, paymentID, work.TransactionID,
			receipt.GasUsed, receipt.EffectiveGasPrice, actor)
	}
	return s.store.MarkTxConfirmed(ctx, paymentID, work.TransactionID,
		receipt.BlockNumber, receipt.BlockHash, receipt.GasUsed, receipt.EffectiveGasPrice, actor)
}

// Observe attaches a heartbeater. Set once at startup, before any worker runs.
func (s *Service) Observe(h Heartbeater) { s.heartbeat = h }

// beat records a completed worker tick, if anything is listening.
func (s *Service) beat(worker string) {
	if s.heartbeat != nil {
		s.heartbeat.Heartbeat(worker, s.now())
	}
}

func (s *Service) release(ctx context.Context, paymentID, worker string) {
	if err := s.store.ReleaseLease(ctx, paymentID, worker); err != nil && !errors.Is(err, ErrLeaseLost) {
		s.logger.WarnContext(ctx, "release payment lease failed", "payment_id", paymentID, "error", err)
	}
}

func settled(payment *verification.Payment, txHash string) *x402.SettleResponse {
	return &x402.SettleResponse{
		Success: true, Transaction: txHash,
		Payer: payment.Payer, Network: config.MainnetNetwork, Amount: payment.Amount,
	}
}

func rejected(reason string, payment *verification.Payment) *x402.SettleResponse {
	response := &x402.SettleResponse{Success: false, ErrorReason: reason, Network: config.MainnetNetwork}
	if payment != nil {
		response.Payer = payment.Payer
		response.Amount = payment.Amount
	}
	return response
}
