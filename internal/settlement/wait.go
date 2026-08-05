package settlement

import (
	"context"
	"strings"
	"time"

	"github.com/ETH402/facilitator/internal/ethereum"
)

// settlementObservation is a finality-qualified chain observation. Receipt is
// retained so the request that first observes a final outcome can durably
// record it before replying; retries must never temporarily contradict an
// outcome that /settle already returned.
type settlementObservation struct {
	kind    settlementObservationKind
	receipt *ethereum.Receipt
}

type settlementObservationKind int

const (
	// observationPending means no final outcome is visible yet: no receipt,
	// a non-canonical receipt, or insufficient confirmation depth.
	observationPending settlementObservationKind = iota
	// observationConfirmed means the receipt is canonical and at least
	// Config.Confirmations deep.
	observationConfirmed
	// observationReverted means a canonical receipt reports status 0 and is at
	// least Config.Confirmations deep. A shallow reverted receipt can be reorged
	// out just like a successful one, so both outcomes use the same finality cut.
	observationReverted
)

// observeSettlement reads the chain once for txHash. Transient RPC failures
// surface as errors so the waiter can keep polling.
func (s *Service) observeSettlement(ctx context.Context, txHash string) (settlementObservation, error) {
	receipt, err := s.chain.TransactionReceipt(ctx, txHash)
	if err != nil {
		return settlementObservation{}, err
	}
	if receipt == nil {
		return settlementObservation{}, nil
	}
	canonical, err := s.chain.BlockByNumber(ctx, &receipt.BlockNumber)
	if err != nil {
		return settlementObservation{}, err
	}
	if !strings.EqualFold(canonical.Hash, receipt.BlockHash) {
		// The receipt's block is not canonical; it is not evidence of any
		// outcome yet.
		return settlementObservation{}, nil
	}
	current, err := s.chain.BlockNumber(ctx)
	if err != nil {
		return settlementObservation{}, err
	}
	depth := uint64(0)
	if current >= receipt.BlockNumber {
		depth = current - receipt.BlockNumber + 1
	}
	if depth < s.cfg.Confirmations {
		return settlementObservation{}, nil
	}
	if receipt.Status == 0 {
		return settlementObservation{kind: observationReverted, receipt: receipt}, nil
	}
	return settlementObservation{kind: observationConfirmed, receipt: receipt}, nil
}

// waitForSettlement polls the chain until the transaction reaches a final
// outcome (confirmed at full depth, or reverted) or the ResponseWait deadline
// passes. Timeout and persistent RPC failure both return observationPending,
// which the caller maps to the broadcast-ack fallback: the durable record and
// the confirmation worker continue regardless of what the HTTP response said.
func (s *Service) waitForSettlement(ctx context.Context, txHash string) settlementObservation {
	if s.cfg.ResponseWait <= 0 {
		return settlementObservation{}
	}
	waitCtx, cancel := context.WithTimeout(ctx, s.cfg.ResponseWait)
	defer cancel()
	poll := time.NewTicker(min(2*time.Second, s.cfg.ResponseWait))
	defer poll.Stop()

	for {
		observation, err := s.observeSettlement(waitCtx, txHash)
		if err != nil {
			s.logger.DebugContext(waitCtx, "settlement confirmation observation failed",
				"tx_hash", txHash, "error", err)
		} else if observation.kind != observationPending {
			return observation
		}
		select {
		case <-waitCtx.Done():
			return settlementObservation{}
		case <-poll.C:
		}
	}
}
