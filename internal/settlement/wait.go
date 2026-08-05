package settlement

import (
	"context"
	"strings"
	"time"
)

// settlementObservation is what the chain says about a broadcast transaction
// at one point in time. It deliberately mirrors the read half of
// Confirmation(): the same canonical-block and depth rules decide what
// /settle may report, but nothing here writes state — finalizing, reverting,
// and reorg unwinding remain the confirmation worker's job.
type settlementObservation int

const (
	// observationPending means no final outcome is visible yet: no receipt,
	// a non-canonical receipt, or insufficient confirmation depth.
	observationPending settlementObservation = iota
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
		return observationPending, err
	}
	if receipt == nil {
		return observationPending, nil
	}
	canonical, err := s.chain.BlockByNumber(ctx, &receipt.BlockNumber)
	if err != nil {
		return observationPending, err
	}
	if !strings.EqualFold(canonical.Hash, receipt.BlockHash) {
		// The receipt's block is not canonical; it is not evidence of any
		// outcome yet.
		return observationPending, nil
	}
	current, err := s.chain.BlockNumber(ctx)
	if err != nil {
		return observationPending, err
	}
	depth := uint64(0)
	if current >= receipt.BlockNumber {
		depth = current - receipt.BlockNumber + 1
	}
	if depth < s.cfg.Confirmations {
		return observationPending, nil
	}
	if receipt.Status == 0 {
		return observationReverted, nil
	}
	return observationConfirmed, nil
}

// waitForSettlement polls the chain until the transaction reaches a final
// outcome (confirmed at full depth, or reverted) or the ResponseWait deadline
// passes. Timeout and persistent RPC failure both return observationPending,
// which the caller maps to the broadcast-ack fallback: the durable record and
// the confirmation worker continue regardless of what the HTTP response said.
func (s *Service) waitForSettlement(ctx context.Context, txHash string) settlementObservation {
	if s.cfg.ResponseWait <= 0 {
		return observationPending
	}
	timeout := time.NewTimer(s.cfg.ResponseWait)
	defer timeout.Stop()
	poll := time.NewTicker(min(2*time.Second, s.cfg.ResponseWait))
	defer poll.Stop()

	for {
		observation, err := s.observeSettlement(ctx, txHash)
		if err != nil {
			s.logger.DebugContext(ctx, "settlement confirmation observation failed",
				"tx_hash", txHash, "error", err)
		} else if observation != observationPending {
			return observation
		}
		select {
		case <-ctx.Done():
			return observationPending
		case <-timeout.C:
			return observationPending
		case <-poll.C:
		}
	}
}
