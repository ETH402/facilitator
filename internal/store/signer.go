package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/ETH402/facilitator/internal/walletproof"
	"github.com/jackc/pgx/v5"
)

// ErrSignerAccountUnknown means no nonce sequence has been seeded for the
// address. Settlement must fail closed rather than guess a starting nonce.
var ErrSignerAccountUnknown = errors.New("signer account has no nonce sequence")

// Querier is the subset of pgx shared by *pgxpool.Pool and pgx.Tx, so nonce
// allocation can join the caller's transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// SeedSignerAccount records the starting nonce for a signer address, taken from
// the chain's transaction count at startup. It is idempotent and never lowers an
// existing sequence: the stored value is authoritative once settlement has begun,
// because it may legitimately be ahead of what the chain reports while
// transactions are still propagating.
func SeedSignerAccount(ctx context.Context, q Querier, signerAddress string, chainNonce uint64) (uint64, error) {
	address, err := walletproof.NormalizeAddress(signerAddress)
	if err != nil {
		return 0, fmt.Errorf("signer address: %w", err)
	}
	var stored string
	err = q.QueryRow(ctx, `
INSERT INTO signer_accounts (signer_address, next_nonce)
VALUES ($1, $2)
ON CONFLICT (signer_address) DO UPDATE
SET next_nonce = greatest(signer_accounts.next_nonce, excluded.next_nonce),
    updated_at = now()
RETURNING next_nonce::text`, address, strconv.FormatUint(chainNonce, 10)).Scan(&stored)
	if err != nil {
		return 0, fmt.Errorf("seed signer account: %w", err)
	}
	return parseNonce(stored)
}

// AllocateNonce reserves the next Ethereum account nonce for a signer address
// and returns it.
//
// Callers must pass the transaction that also persists the settlement intent
// using the nonce. The single UPDATE takes a row lock, so concurrent allocations
// for the same address serialise and no two callers receive the same nonce; the
// shared transaction is what guarantees a nonce is never consumed without a
// durable record of the transaction that owns it. Replacements deliberately
// reuse their original nonce and must not call this.
func AllocateNonce(ctx context.Context, q Querier, signerAddress string) (uint64, error) {
	address, err := walletproof.NormalizeAddress(signerAddress)
	if err != nil {
		return 0, fmt.Errorf("signer address: %w", err)
	}
	var allocated string
	err = q.QueryRow(ctx, `
UPDATE signer_accounts
SET next_nonce = next_nonce + 1, updated_at = now()
WHERE signer_address = $1
RETURNING (next_nonce - 1)::text`, address).Scan(&allocated)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrSignerAccountUnknown
	}
	if err != nil {
		return 0, fmt.Errorf("allocate nonce: %w", err)
	}
	return parseNonce(allocated)
}

// parseNonce converts the numeric column's text form. The column is wider than
// uint64 to match ethereum_transactions.transaction_nonce, so a value outside
// uint64 is a corrupted sequence rather than a usable nonce.
func parseNonce(value string) (uint64, error) {
	nonce, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("nonce %q is not a uint64: %w", value, err)
	}
	return nonce, nil
}
