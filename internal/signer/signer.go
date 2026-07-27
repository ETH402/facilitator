package signer

import (
	"context"
	"errors"
	"math/big"
)

var ErrDisabled = errors.New("transaction signing is disabled in this build")

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
}

// Validate rejects transactions that are unsafe to sign regardless of backend.
// It is a last line of defence, not a substitute for the gas policy enforced in
// configuration: a signer must never be handed an unbounded spend.
func (t Transaction) Validate() error {
	var errs []error
	if t.ChainID != 1 {
		errs = append(errs, errors.New("only Ethereum mainnet may be signed"))
	}
	if t.To == "" {
		errs = append(errs, errors.New("recipient is required"))
	}
	if len(t.Data) == 0 {
		errs = append(errs, errors.New("calldata is required"))
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

type SignedTransaction struct {
	Raw []byte
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
