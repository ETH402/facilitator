package signer

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"strings"

	"github.com/ETH402/facilitator/internal/config"
	"github.com/ETH402/facilitator/internal/policy"
	"golang.org/x/crypto/sha3"
)

var ErrDisabled = errors.New("transaction signing is disabled in this build")

// transferWithAuthorizationSelector is the only function this service may ever
// ask a signer to call.
//
// It is derived here from the canonical EIP-3009 v/r/s signature rather than
// shared with the calldata builder, so the allowlist is an independent check on
// what gets signed instead of a restatement of the code it guards. A
// cross-check test asserts it matches what the builder actually packs.
var transferWithAuthorizationSelector = functionSelector(
	"transferWithAuthorization(address,address,uint256,uint256,uint256,bytes32,uint8,bytes32,bytes32)")

func functionSelector(signature string) []byte {
	keccak := sha3.NewLegacyKeccak256()
	keccak.Write([]byte(signature))
	return keccak.Sum(nil)[:4]
}

// Transaction is a fully determined EIP-1559 transaction. Every field is an
// input: the signer contributes a signature and nothing else.
//
// Nonce is supplied by the caller because ADR-0004 requires it to be allocated
// and committed before signing, so a signer that chose its own nonce could
// produce a transaction no durable record owns. Value and the fee fields are
// decimal wei strings, keeping money in integer arithmetic end to end.
type Transaction struct {
	ChainID              uint64
	Nonce                uint64
	To                   string
	Data                 []byte
	Value                string
	GasLimit             uint64
	MaxFeePerGas         string
	MaxPriorityFeePerGas string

	// Authorization is the EIP-3009 authorization Data was packed from. The
	// policy-boundary backend needs it because that boundary builds calldata
	// itself rather than trusting what this process packed; every other backend
	// ignores it. Optional so backends that sign calldata directly still work.
	Authorization *policy.Authorization
}

// Validate rejects transactions that are unsafe to sign regardless of backend.
// It is a last line of defence, not a substitute for the gas policy enforced in
// configuration: a signer must never be handed an unbounded spend.
func (t Transaction) Validate() error {
	var errs []error
	if t.ChainID != 1 {
		errs = append(errs, errors.New("only Ethereum mainnet may be signed"))
	}
	// The calldata allowlist. Cloud KMS signs opaque digests and cannot inspect
	// calldata, so this is the only thing standing between a compromised process
	// and an arbitrary signed transaction (ADR-0004 decision 8); loss is
	// otherwise bounded only by the signer's hot balance.
	if !strings.EqualFold(t.To, config.MainnetUSDC) {
		errs = append(errs, errors.New("only canonical Ethereum-mainnet USDC may be called"))
	}
	if len(t.Data) < 4 || !bytes.Equal(t.Data[:4], transferWithAuthorizationSelector) {
		errs = append(errs, errors.New("only transferWithAuthorization calldata may be signed"))
	}
	if t.GasLimit == 0 {
		errs = append(errs, errors.New("gas limit must be non-zero"))
	}
	value, valueOK := new(big.Int).SetString(t.Value, 10)
	if !valueOK || value.Sign() != 0 {
		// USDC settlement never transfers ether; a non-zero value would mean the
		// calldata and the intent disagree.
		errs = append(errs, errors.New("value must be exactly zero wei"))
	}
	maxFee, maxFeeOK := new(big.Int).SetString(t.MaxFeePerGas, 10)
	priorityFee, priorityFeeOK := new(big.Int).SetString(t.MaxPriorityFeePerGas, 10)
	if !maxFeeOK || maxFee.Sign() <= 0 {
		errs = append(errs, errors.New("max fee per gas must be a positive decimal wei string"))
	}
	if !priorityFeeOK || priorityFee.Sign() < 0 {
		errs = append(errs, errors.New("max priority fee per gas must be an unsigned decimal wei string"))
	}
	if maxFeeOK && priorityFeeOK && maxFee.Sign() > 0 && priorityFee.Cmp(maxFee) > 0 {
		errs = append(errs, errors.New("max priority fee per gas must not exceed max fee per gas"))
	}
	return errors.Join(errs...)
}

// SignedTransaction is the signed payload plus the digest that was signed.
//
// SigHash is the keccak hash of the unsigned EIP-1559 transaction — the exact
// digest the signature commits to. It is deterministic: fully derived from the
// transaction fields, unlike Raw, whose encoding embeds the signature and
// therefore differs whenever the backend randomizes the ECDSA nonce (Cloud KMS
// does). Settlement recovery persists it to prove a re-signed transaction is
// the recorded one (ADR-0004 decision 4).
type SignedTransaction struct {
	Raw     []byte
	SigHash [32]byte
}

type Signer interface {
	Address(context.Context) (string, error)
	SignTransaction(context.Context, Transaction) (SignedTransaction, error)
}

type Disabled struct{}

func (Disabled) Address(context.Context) (string, error) { return "", ErrDisabled }
func (Disabled) SignTransaction(context.Context, Transaction) (SignedTransaction, error) {
	return SignedTransaction{}, ErrDisabled
}

// TestSigner is deliberately incapable of producing an Ethereum transaction.
// It exists to test dependency wiring without any funded key, and applies the
// same validation a real signer must so that a caller which forgets to populate
// the nonce or gas policy fails in tests rather than in production.
type TestSigner struct{ TestAddress string }

func (s TestSigner) Address(context.Context) (string, error) { return s.TestAddress, nil }
func (s TestSigner) SignTransaction(_ context.Context, tx Transaction) (SignedTransaction, error) {
	if err := tx.Validate(); err != nil {
		return SignedTransaction{}, err
	}
	return SignedTransaction{Raw: []byte("test-only-not-broadcastable")}, nil
}
