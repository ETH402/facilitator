package settlement

import "errors"

// ErrLeaseUnavailable means another worker holds a live lease on the payment.
// It is not a failure: the holder is already doing the work, so the caller
// should observe the recorded state rather than compete for it.
var ErrLeaseUnavailable = errors.New("payment is leased by another worker")

// Work is everything the broadcast pipeline needs for one payment: the
// calldata fields from the durable payment record plus the active transaction
// row that owns the Ethereum nonce. It is loaded after the lease is taken, so
// only one worker ever acts on it at a time.
type Work struct {
	Lease
	Authorization Authorization

	TransactionID     string
	TransactionStatus string
	Nonce             uint64

	// TxHash is empty until a broadcast is durably recorded. A broadcasting
	// transaction without it is the ambiguous crash case of ADR-0004 decision
	// 4: it may have reached the network, so it is never re-broadcast.
	TxHash string
}

// BroadcastPending reports whether the transaction still needs signing and
// broadcast. Only the committed intent state may be broadcast; anything
// further along either has a hash or is ambiguous.
func (w Work) BroadcastPending() bool { return w.TransactionStatus == "intent" }

// AmbiguousBroadcast reports the crash window between broadcast and recording
// the hash: signed (raw hash stored) but no transaction hash. Recovery
// resolves it on chain; nothing in the broadcast path may touch it again.
func (w Work) AmbiguousBroadcast() bool {
	return w.TransactionStatus == "broadcasting" && w.TxHash == ""
}
