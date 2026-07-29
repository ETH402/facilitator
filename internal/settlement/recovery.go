package settlement

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"time"

	"github.com/ETH402/facilitator/internal/config"
	"github.com/ETH402/facilitator/internal/signer"
	"golang.org/x/crypto/sha3"
)

// RecoveryWorker resolves what the broadcast and confirmation pipelines
// cannot: ambiguous broadcasts (the send outcome was never recorded), stuck
// pending transactions (fee-bumped replacements sharing the nonce), nonce
// gaps left by dropped expired intents, and superseded originals the network
// mined anyway. It never finalizes a payment itself — recovery re-attaches
// hashes or puts transactions back into the broadcast pipeline, and the
// confirmation worker observes them from there (ADR-0004 decision 4).
type RecoveryWorker struct {
	identity string
	service  *Service
	logger   *slog.Logger
	now      func() time.Time
}

func (s *Service) RecoveryWorker() *RecoveryWorker {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "unknown-host"
	}
	return &RecoveryWorker{
		identity: fmt.Sprintf("recovery/%s/%d", hostname, os.Getpid()),
		service:  s,
		logger:   s.logger,
		now:      s.now,
	}
}

// Run recovers until the context is cancelled, on the same tick cadence as
// the other settlement workers.
func (w *RecoveryWorker) Run(ctx context.Context) {
	tick := func() { guard(ctx, w.logger, "recovery", "tick", func() { w.process(ctx) }) }
	tick()
	ticker := time.NewTicker(w.service.cfg.WorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}

// process runs the four recovery concerns in dependency order: leased
// payments first (they may re-enter the active pipeline), then the
// query-based watches that need no lease because no other worker touches
// those rows.
// Each pass is guarded independently so a panic in one concern does not skip
// the other three, which resolve unrelated stuck transactions.
func (w *RecoveryWorker) process(ctx context.Context) {
	guard(ctx, w.logger, "recovery", "leased", func() { w.recoverLeased(ctx) })
	guard(ctx, w.logger, "recovery", "replacements", func() { w.observeReplacements(ctx) })
	guard(ctx, w.logger, "recovery", "nonce-gaps", func() { w.fillNonceGaps(ctx) })
	guard(ctx, w.logger, "recovery", "gap-fillers", func() { w.observeGapFillers(ctx) })
}

// recoverLeased claims payments that may need intervention: manual_review
// holds ambiguous broadcasts, broadcast holds stuck pendings, replaced holds
// stuck replacements (a replacement can stall too and be re-bumped).
func (w *RecoveryWorker) recoverLeased(ctx context.Context) {
	leases, err := w.service.store.ClaimPayments(ctx, ClaimRequest{
		Worker:   w.identity,
		States:   []State{StateManualReview, StateBroadcast, StateReplaced},
		Duration: w.service.cfg.LeaseDuration,
		Limit:    workerBatch,
		Now:      w.now(),
	})
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			w.logger.ErrorContext(ctx, "claim payments failed", "worker", "recovery", "error", err)
		}
		return
	}
	for _, lease := range leases {
		if ctx.Err() != nil {
			return
		}
		guard(ctx, w.logger, "recovery", "recover-payment", func() {
			if err := w.service.recoverPayment(ctx, lease.PaymentID, "worker"); err != nil {
				w.logger.WarnContext(ctx, "recover payment failed",
					"payment_id", lease.PaymentID,
					"payment_identity", lease.PaymentIdentity, "error", err)
			}
		})
		w.service.release(ctx, lease.PaymentID, w.identity)
	}
}

// recoverPayment dispatches on the active transaction's status: ambiguous
// transactions are reconciled on chain, stale broadcasts are fee-bumped.
// Anything else (a fresh broadcast still inside its window) is left to the
// confirmation worker.
func (s *Service) recoverPayment(ctx context.Context, paymentID, actor string) error {
	work, err := s.store.LoadSettlementWork(ctx, paymentID)
	if err != nil {
		return fmt.Errorf("load settlement work: %w", err)
	}
	switch {
	case work.TransactionStatus == "ambiguous":
		return s.resolveAmbiguous(ctx, work, actor)
	case work.TransactionStatus == "broadcast" &&
		!work.BroadcastAttemptedAt.IsZero() &&
		s.now().Sub(work.BroadcastAttemptedAt) > s.cfg.ReplacementAfter:
		return s.replaceStuck(ctx, work, actor)
	}
	return nil
}

// resolveAmbiguous settles the crash window of ADR-0004 decision 4. The
// signed transaction's keccak is its transaction hash, so the stored raw hash
// is the lookup key: a receipt or a mempool sighting re-attaches the hash and
// returns the payment to the broadcast pipeline. Only after the grace window
// — and only when the exact signing inputs were persisted — may the identical
// transaction be re-signed and re-broadcast. Identity is proven by the
// sighash, which is deterministic across signers; the re-signed bytes may
// still differ (Cloud KMS randomizes the ECDSA nonce), and a differing hash
// is then recorded replacement-shaped, so the network mining either signature
// resolves the payment instead of wedging it.
func (s *Service) resolveAmbiguous(ctx context.Context, work Work, actor string) error {
	if work.RawHash == "" {
		return errors.New("ambiguous transaction has no raw hash; manual reconciliation required")
	}
	txHash := "0x" + work.RawHash
	receipt, err := s.chain.TransactionReceipt(ctx, txHash)
	if err != nil {
		return fmt.Errorf("fetch receipt: %w", err)
	}
	if receipt != nil {
		return s.store.MarkTxRecoveredBroadcast(ctx, work.PaymentID, work.TransactionID, txHash, actor)
	}
	pending, err := s.chain.TransactionByHash(ctx, txHash)
	if err != nil {
		return fmt.Errorf("fetch transaction: %w", err)
	}
	if pending != nil {
		return s.store.MarkTxRecoveredBroadcast(ctx, work.PaymentID, work.TransactionID, txHash, actor)
	}
	if s.now().Sub(work.TransactionUpdatedAt) < s.cfg.RecoveryGrace {
		return nil // Inside the grace window: the broadcast may still propagate.
	}
	if work.MaxFeePerGas == "" || work.MaxPriorityFeePerGas == "" || work.GasLimit == 0 {
		return errors.New("ambiguous transaction predates stored fee fields; manual reconciliation required")
	}
	raw, rawHash, err := s.signIdentical(ctx, work)
	if err != nil {
		return err
	}
	if rawHash != work.RawHash {
		// The sighash matched, so this is the identical transaction signed
		// with fresh ECDSA randomness: same nonce, gas, fees, and calldata,
		// different hash. Record it as the replacement of the original before
		// sending — a send failure then leaves a durable row the stuck
		// pipeline re-bumps from, and observeReplacements resolves the
		// payment if the network mines the original signature instead.
		replacement := Replacement{
			Nonce:         work.Nonce,
			TxHash:        "0x" + rawHash,
			RawHash:       rawHash,
			GasLimit:      work.GasLimit,
			MaxFee:        work.MaxFeePerGas,
			PriorityFee:   work.MaxPriorityFeePerGas,
			SignerAddress: work.SignerAddress,
		}
		if err := s.store.MarkTxAmbiguousReplaced(ctx, work.PaymentID, work.TransactionID, replacement, actor); err != nil {
			return fmt.Errorf("record re-signed transaction: %w", err)
		}
		if _, err := s.chain.SendRawTransaction(ctx, "0x"+hex.EncodeToString(raw)); err != nil {
			return fmt.Errorf("broadcast re-signed transaction: %w", err)
		}
		return nil
	}
	if _, err := s.chain.SendRawTransaction(ctx, "0x"+hex.EncodeToString(raw)); err != nil {
		// Unknown outcome again: stay ambiguous. The next tick's on-chain
		// lookup finds the transaction if this attempt reached the network —
		// the bytes are identical, so the lookup key is unchanged.
		return fmt.Errorf("re-broadcast ambiguous transaction: %w", err)
	}
	return s.store.MarkTxRecoveredBroadcast(ctx, work.PaymentID, work.TransactionID, txHash, actor)
}

// signIdentical reproduces the exact transaction a row records — same nonce,
// gas, fees, and calldata — and proves identity before returning the raw
// bytes and their hash. The proof is the sighash: the digest the signature
// commits to, fully determined by the transaction fields, so every backend
// reproduces it. Rows written before migration 000006 have no stored sighash
// and fall back to comparing the raw transaction hash, which only a
// deterministic signer can satisfy — with Cloud KMS that comparison refuses,
// which is the safe outcome for a record that cannot be verified.
func (s *Service) signIdentical(ctx context.Context, work Work) (raw []byte, rawHash string, err error) {
	calldata, err := TransferWithAuthorizationData(work.Authorization)
	if err != nil {
		return nil, "", fmt.Errorf("build calldata: %w", err)
	}
	signCtx, cancel := context.WithTimeout(ctx, s.cfg.SigningTimeout)
	signed, err := s.signer.SignTransaction(signCtx, signer.Transaction{
		ChainID:              config.MainnetChainID,
		Nonce:                work.Nonce,
		To:                   config.MainnetUSDC,
		Data:                 calldata,
		Value:                "0",
		GasLimit:             work.GasLimit,
		MaxFeePerGas:         work.MaxFeePerGas,
		MaxPriorityFeePerGas: work.MaxPriorityFeePerGas,
	})
	cancel()
	if err != nil {
		return nil, "", fmt.Errorf("re-sign transaction: %w", err)
	}
	if work.Sighash != "" {
		if recomputed := hex.EncodeToString(signed.SigHash[:]); recomputed != work.Sighash {
			return nil, "", fmt.Errorf("re-signed sighash %s does not match stored %s; refusing to broadcast", recomputed, work.Sighash)
		}
	}
	keccak := sha3.NewLegacyKeccak256()
	keccak.Write(signed.Raw)
	rawHash = hex.EncodeToString(keccak.Sum(nil))
	if work.Sighash == "" && rawHash != work.RawHash {
		return nil, "", fmt.Errorf("re-signed hash %s does not match stored %s; refusing to broadcast", rawHash, work.RawHash)
	}
	return signed.Raw, rawHash, nil
}

// replaceStuck supersedes a pending transaction with a fee-bumped replacement
// on the same nonce. The replacement is recorded before it is sent: a send
// failure then leaves a durable row the next stuck tick re-bumps from, and a
// mined original retires the row via observeReplacements — recording after
// sending could instead loop forever on the mempool's price-bump rule. When
// the ceiling leaves no headroom the transaction is left pending; raising the
// ceiling is an operator decision, not the worker's.
func (s *Service) replaceStuck(ctx context.Context, work Work, actor string) error {
	if work.MaxFeePerGas == "" || work.MaxPriorityFeePerGas == "" || work.GasLimit == 0 {
		s.logger.WarnContext(ctx, "stuck transaction predates stored fee fields; cannot bump",
			"payment_id", work.PaymentID, "transaction_id", work.TransactionID)
		return nil
	}
	oldMax, ok := new(big.Int).SetString(work.MaxFeePerGas, 10)
	if !ok {
		return fmt.Errorf("stored max fee %q is not a decimal integer", work.MaxFeePerGas)
	}
	oldTip, ok := new(big.Int).SetString(work.MaxPriorityFeePerGas, 10)
	if !ok {
		return fmt.Errorf("stored priority fee %q is not a decimal integer", work.MaxPriorityFeePerGas)
	}
	block, err := s.chain.BlockByNumber(ctx, nil)
	if err != nil {
		return fmt.Errorf("fetch latest block: %w", err)
	}
	baseFee, ok := new(big.Int).SetString(block.BaseFee, 10)
	if !ok {
		return fmt.Errorf("latest block reports invalid base fee %q", block.BaseFee)
	}
	ceiling, ok := new(big.Int).SetString(s.cfg.MaxFeePerGas, 10)
	if !ok {
		return fmt.Errorf("configured max fee %q is not a decimal integer", s.cfg.MaxFeePerGas)
	}
	maxFee, priority, ok := BumpFees(oldMax, oldTip, baseFee, ceiling)
	if !ok {
		s.logger.InfoContext(ctx, "no fee headroom to bump stuck transaction; still waiting",
			"payment_id", work.PaymentID, "transaction_id", work.TransactionID,
			"tx_hash", work.TxHash)
		return nil
	}
	calldata, err := TransferWithAuthorizationData(work.Authorization)
	if err != nil {
		return fmt.Errorf("build calldata: %w", err)
	}
	signCtx, cancel := context.WithTimeout(ctx, s.cfg.SigningTimeout)
	signed, err := s.signer.SignTransaction(signCtx, signer.Transaction{
		ChainID:              config.MainnetChainID,
		Nonce:                work.Nonce,
		To:                   config.MainnetUSDC,
		Data:                 calldata,
		Value:                "0",
		GasLimit:             work.GasLimit,
		MaxFeePerGas:         maxFee.String(),
		MaxPriorityFeePerGas: priority.String(),
	})
	cancel()
	if err != nil {
		return fmt.Errorf("sign replacement: %w", err)
	}
	keccak := sha3.NewLegacyKeccak256()
	keccak.Write(signed.Raw)
	rawHash := hex.EncodeToString(keccak.Sum(nil))
	replacement := Replacement{
		Nonce:         work.Nonce,
		TxHash:        "0x" + rawHash,
		RawHash:       rawHash,
		GasLimit:      work.GasLimit,
		MaxFee:        maxFee.String(),
		PriorityFee:   priority.String(),
		SignerAddress: work.SignerAddress,
	}
	if err := s.store.MarkTxReplaced(ctx, work.PaymentID, work.TransactionID, replacement, actor); err != nil {
		return fmt.Errorf("record replacement: %w", err)
	}
	if _, err := s.chain.SendRawTransaction(ctx, "0x"+hex.EncodeToString(signed.Raw)); err != nil {
		return fmt.Errorf("broadcast replacement: %w", err)
	}
	return nil
}

// observeReplacements checks whether the network mined an original
// transaction instead of its replacement. Either can win the nonce; when the
// original lands, the recorded history is corrected and the never-minable
// replacement is dropped.
func (w *RecoveryWorker) observeReplacements(ctx context.Context) {
	tracked, err := w.service.store.ListReplacedPending(ctx)
	if err != nil {
		w.logger.WarnContext(ctx, "list replaced transactions failed", "error", err)
		return
	}
	for _, t := range tracked {
		if ctx.Err() != nil {
			return
		}
		receipt, err := w.service.chain.TransactionReceipt(ctx, t.TxHash)
		if err != nil {
			w.logger.WarnContext(ctx, "fetch replaced transaction receipt failed",
				"payment_id", t.PaymentID, "tx_hash", t.TxHash, "error", err)
			continue
		}
		if receipt == nil {
			continue
		}
		if err := w.service.store.MarkReplacementLanded(ctx, t.PaymentID, t.TransactionID,
			receipt.Status == 1, receipt.BlockNumber, receipt.BlockHash,
			receipt.GasUsed, receipt.EffectiveGasPrice, "worker"); err != nil {
			w.logger.WarnContext(ctx, "record landed original failed",
				"payment_id", t.PaymentID, "tx_hash", t.TxHash, "error", err)
		}
	}
}

// fillNonceGaps re-broadcasts the original intent of a dropped expired
// transaction when a later nonce of the same signer is in flight: Ethereum
// mines nonces in order, so the gap blocks everything behind it. The
// authorization is expired, so the filler predictably reverts — the revert
// still consumes the nonce, which is the entire point. A dropped nonce with
// nothing behind it is left alone.
func (w *RecoveryWorker) fillNonceGaps(ctx context.Context) {
	works, err := w.service.store.ListDroppedBlockingGaps(ctx, w.service.cfg.SignerAddress)
	if err != nil {
		w.logger.WarnContext(ctx, "list nonce gaps failed", "error", err)
		return
	}
	for _, work := range works {
		if ctx.Err() != nil {
			return
		}
		if err := w.service.fillNonceGap(ctx, work); err != nil {
			w.logger.WarnContext(ctx, "fill nonce gap failed",
				"payment_id", work.PaymentID, "nonce", work.Nonce, "error", err)
		}
	}
}

func (s *Service) fillNonceGap(ctx context.Context, work Work) error {
	calldata, err := TransferWithAuthorizationData(work.Authorization)
	if err != nil {
		return fmt.Errorf("build calldata: %w", err)
	}
	maxFee, priority, err := s.estimateFees(ctx)
	if err != nil {
		return fmt.Errorf("estimate fees: %w", err)
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
		MaxPriorityFeePerGas: priority.String(),
	})
	cancel()
	if err != nil {
		return fmt.Errorf("sign gap filler: %w", err)
	}
	keccak := sha3.NewLegacyKeccak256()
	keccak.Write(signed.Raw)
	rawHash := hex.EncodeToString(keccak.Sum(nil))
	txHash := "0x" + rawHash
	// The fill is deterministic, so a previous attempt that reached the
	// network without being recorded is visible under the same hash: record
	// it instead of resending into a certain "already known" rejection.
	existing, err := s.chain.TransactionByHash(ctx, txHash)
	if err != nil {
		return fmt.Errorf("fetch gap filler transaction: %w", err)
	}
	if existing == nil {
		if _, err := s.chain.SendRawTransaction(ctx, "0x"+hex.EncodeToString(signed.Raw)); err != nil {
			return fmt.Errorf("broadcast gap filler: %w", err)
		}
	}
	return s.store.MarkGapFillerBroadcast(ctx, work.TransactionID, rawHash, txHash,
		s.cfg.GasLimit, maxFee.String(), priority.String())
}

// observeGapFillers retires gap fillers from their receipts. Their payments
// are expired, so the confirmation worker never sees them. A successful
// filler is an anomaly — the chain accepted an authorization the facilitator
// considers expired — and is flagged loudly for a human rather than resolved
// automatically.
func (w *RecoveryWorker) observeGapFillers(ctx context.Context) {
	tracked, err := w.service.store.ListGapFillers(ctx)
	if err != nil {
		w.logger.WarnContext(ctx, "list gap fillers failed", "error", err)
		return
	}
	for _, t := range tracked {
		if ctx.Err() != nil {
			return
		}
		receipt, err := w.service.chain.TransactionReceipt(ctx, t.TxHash)
		if err != nil {
			w.logger.WarnContext(ctx, "fetch gap filler receipt failed",
				"payment_id", t.PaymentID, "tx_hash", t.TxHash, "error", err)
			continue
		}
		if receipt == nil {
			continue
		}
		if receipt.Status == 1 {
			// The chain accepted an authorization believed expired, so USDC moved
			// and the record disagrees with the ledger. Escalate once rather than
			// re-reporting every tick, and leave the reconciliation to a human.
			w.logger.ErrorContext(ctx, "gap filler succeeded on an expired authorization; escalating to manual review",
				"payment_id", t.PaymentID, "transaction_id", t.TransactionID,
				"tx_hash", t.TxHash, "block_number", receipt.BlockNumber)
			if err := w.service.store.MarkGapFillerSucceeded(ctx, t.PaymentID, t.TransactionID,
				receipt.BlockNumber, receipt.BlockHash, receipt.GasUsed,
				receipt.EffectiveGasPrice, "worker"); err != nil {
				w.logger.WarnContext(ctx, "escalate succeeded gap filler failed",
					"payment_id", t.PaymentID, "tx_hash", t.TxHash, "error", err)
			}
			continue
		}
		if err := w.service.store.MarkGapFillerResolved(ctx, t.TransactionID,
			receipt.GasUsed, receipt.EffectiveGasPrice); err != nil {
			w.logger.WarnContext(ctx, "resolve gap filler failed",
				"payment_id", t.PaymentID, "tx_hash", t.TxHash, "error", err)
		}
	}
}
