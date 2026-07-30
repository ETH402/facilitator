package settlement

import (
	"errors"
	"time"
)

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

	// Recovery fields. RawHash identifies the signed transaction on chain.
	// Sighash is the deterministic digest the signature commits to: re-signing
	// the identical transaction reproduces it under every backend, whereas
	// RawHash changes when the signer randomizes the ECDSA nonce (Cloud KMS
	// does), so recovery proves identity by Sighash (ADR-0004 decision 4).
	// The fee and timing fields reconstruct the identical transaction (for
	// ambiguous resolution) or its replacement (for stuck pending); they are
	// empty for rows written before migration 000004, which recovery resolves
	// by on-chain lookup only. Sighash is empty for rows before 000006; those
	// fall back to the raw-hash comparison, which only a deterministic signer
	// can satisfy.
	RawHash              string
	Sighash              string
	SignerAddress        string
	GasLimit             uint64
	MaxFeePerGas         string
	MaxPriorityFeePerGas string
	BroadcastAttemptedAt time.Time

	// TransactionUpdatedAt is the last write to the transaction row. Recovery
	// uses it as the ambiguity clock: only after the backoff window may an
	// ambiguous transaction be re-broadcast identically.
	TransactionUpdatedAt time.Time

	// AmbiguousAttempts counts failed identical re-broadcasts of an ambiguous
	// transaction. Each attempt is a paid KMS signing operation, so recovery
	// backs off exponentially per attempt instead of re-signing every tick.
	AmbiguousAttempts int

	// DBNow is the database clock reading taken when this row was loaded.
	// The persisted timestamps above are stamped by the database, so intervals
	// measured against them must use the same clock: the application clock can
	// be skewed, and mixing the two makes those intervals lie.
	DBNow time.Time
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

// Replacement is a fee-bumped re-broadcast of a stuck transaction. It reuses
// the original nonce by design (ADR-0004 decision 1 forbids allocating a
// fresh one) and carries its own hash, so either transaction may be the one
// that mines.
type Replacement struct {
	Nonce         uint64
	TxHash        string
	RawHash       string
	GasLimit      uint64
	MaxFee        string
	PriorityFee   string
	SignerAddress string
}

// TrackedTransaction is a transaction outside the active set whose hash must
// still be watched: a superseded replacement (the network may mine either
// version) or a nonce gap-filler attached to an expired payment.
type TrackedTransaction struct {
	PaymentID     string
	TransactionID string
	TxHash        string
	// Status is populated for nonce-gap fillers, whose replaced originals stay
	// watched alongside the active replacement: only a "broadcast" filler may
	// be re-broadcast, since the replacement already supersedes a "replaced"
	// one at a lower fee.
	Status string
	// RawTransaction is populated for a prepared nonce-gap filler. Persisting
	// the exact signed bytes makes retry safe with randomized KMS signatures.
	RawTransaction []byte
}
