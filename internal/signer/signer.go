package signer

import (
	"context"
	"errors"
)

var ErrDisabled = errors.New("transaction signing is disabled in Milestone 0")

type Transaction struct {
	ChainID uint64
	To      string
	Data    []byte
	Value   string
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
// It exists to test dependency wiring without any funded key.
type TestSigner struct{ TestAddress string }

func (s TestSigner) Address(context.Context) (string, error) { return s.TestAddress, nil }
func (s TestSigner) SignTransaction(_ context.Context, tx Transaction) (SignedTransaction, error) {
	if tx.ChainID != 1 {
		return SignedTransaction{}, errors.New("test signer rejected non-mainnet chain")
	}
	return SignedTransaction{Raw: []byte("test-only-not-broadcastable")}, nil
}
