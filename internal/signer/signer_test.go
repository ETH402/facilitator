package signer

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// settlementCalldata is transferWithAuthorization calldata: the real selector
// followed by nine 32-byte words. Only the selector is inspected by the
// allowlist, but the shape matches what the calldata builder produces.
func settlementCalldata() []byte {
	data := make([]byte, 4+9*32)
	copy(data, transferWithAuthorizationSelector)
	return data
}

func validTransaction() Transaction {
	return Transaction{
		ChainID: 1, Nonce: 7,
		To:                   "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
		Data:                 settlementCalldata(),
		Value:                "0",
		GasLimit:             120000,
		MaxFeePerGas:         "30000000000",
		MaxPriorityFeePerGas: "1000000000",
	}
}

// The allowlist is the only barrier between a compromised process and an
// arbitrary signed transaction, because Cloud KMS cannot inspect calldata.
func TestValidateEnforcesCalldataAllowlist(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*Transaction){
		"another contract":   func(tx *Transaction) { tx.To = "0x1111111111111111111111111111111111111111" },
		"ether transfer":     func(tx *Transaction) { tx.To = "0x1111111111111111111111111111111111111111"; tx.Data = nil },
		"wrong selector":     func(tx *Transaction) { tx.Data = append([]byte{0xa9, 0x05, 0x9c, 0xbb}, tx.Data[4:]...) },
		"truncated calldata": func(tx *Transaction) { tx.Data = tx.Data[:3] },
	}
	for name, mutate := range cases {
		tx := validTransaction()
		mutate(&tx)
		if err := tx.Validate(); err == nil {
			t.Fatalf("%s was accepted", name)
		}
		if _, err := (TestSigner{}).SignTransaction(context.Background(), tx); err == nil {
			t.Fatalf("%s was signed", name)
		}
	}
	// The canonical address in mixed-case checksum form must still be accepted.
	tx := validTransaction()
	tx.To = "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"
	if err := tx.Validate(); err != nil {
		t.Fatalf("checksummed USDC address rejected: %v", err)
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
		"foreign recipient":     func(tx *Transaction) { tx.To = "0x2222222222222222222222222222222222222222" },
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
	for _, want := range []string{"mainnet", "USDC", "transferWithAuthorization", "gas limit", "zero wei", "max fee per gas"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}
