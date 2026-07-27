package signer

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func validTransaction() Transaction {
	return Transaction{
		ChainID: 1, Nonce: 7,
		To:                   "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
		Data:                 []byte{0xe3, 0xee, 0x39, 0xc4},
		Value:                "0",
		GasLimit:             120000,
		MaxFeePerGas:         "30000000000",
		MaxPriorityFeePerGas: "1000000000",
	}
}

func TestDisabledSignerFailsClosed(t *testing.T) {
	t.Parallel()
	if _, err := (Disabled{}).SignTransaction(context.Background(), validTransaction()); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled signer returned %v, want ErrDisabled", err)
	}
	if _, err := (Disabled{}).Address(context.Background()); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled address returned %v, want ErrDisabled", err)
	}
}

func TestTestSignerNeverProducesBroadcastableTransaction(t *testing.T) {
	t.Parallel()
	signed, err := TestSigner{TestAddress: "0x1111111111111111111111111111111111111111"}.
		SignTransaction(context.Background(), validTransaction())
	if err != nil {
		t.Fatal(err)
	}
	if string(signed.Raw) != "test-only-not-broadcastable" {
		t.Fatalf("test signer produced %q", signed.Raw)
	}
}

func TestTransactionValidateRejectsUnsafeTransactions(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*Transaction){
		"non-mainnet chain":     func(tx *Transaction) { tx.ChainID = 8453 },
		"missing recipient":     func(tx *Transaction) { tx.To = "" },
		"missing calldata":      func(tx *Transaction) { tx.Data = nil },
		"zero gas limit":        func(tx *Transaction) { tx.GasLimit = 0 },
		"non-zero value":        func(tx *Transaction) { tx.Value = "1" },
		"malformed value":       func(tx *Transaction) { tx.Value = "" },
		"zero max fee":          func(tx *Transaction) { tx.MaxFeePerGas = "0" },
		"malformed max fee":     func(tx *Transaction) { tx.MaxFeePerGas = "0x10" },
		"negative priority fee": func(tx *Transaction) { tx.MaxPriorityFeePerGas = "-1" },
		"priority above max":    func(tx *Transaction) { tx.MaxPriorityFeePerGas = "30000000001" },
	}
	for name, mutate := range cases {
		tx := validTransaction()
		mutate(&tx)
		if err := tx.Validate(); err == nil {
			t.Fatalf("%s was accepted", name)
		}
		// The same transaction must also be refused by a signer, not merely by a
		// caller that remembers to validate first.
		if _, err := (TestSigner{}).SignTransaction(context.Background(), tx); err == nil {
			t.Fatalf("%s was signed", name)
		}
	}
	if err := validTransaction().Validate(); err != nil {
		t.Fatalf("valid transaction rejected: %v", err)
	}
}

// A zero nonce is legitimate: it is the first transaction a fresh signer sends.
func TestZeroNonceIsValid(t *testing.T) {
	t.Parallel()
	tx := validTransaction()
	tx.Nonce = 0
	if err := tx.Validate(); err != nil {
		t.Fatalf("zero nonce rejected: %v", err)
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	t.Parallel()
	err := Transaction{}.Validate()
	if err == nil {
		t.Fatal("empty transaction accepted")
	}
	for _, want := range []string{"mainnet", "recipient", "calldata", "gas limit", "zero wei", "max fee per gas"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}
