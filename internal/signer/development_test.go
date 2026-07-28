package signer

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

func testTransaction() Transaction {
	return Transaction{
		ChainID:              1,
		Nonce:                7,
		To:                   "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
		Data:                 []byte{0x01, 0x02},
		Value:                "0",
		GasLimit:             100000,
		MaxFeePerGas:         "30000000000",
		MaxPriorityFeePerGas: "2000000000",
	}
}

func TestDevelopmentAddress(t *testing.T) {
	t.Parallel()
	s, err := NewDevelopment("0x" + strings.Repeat("00", 31) + "01")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	address, err := s.Address(context.Background())
	if err != nil {
		t.Fatalf("address: %v", err)
	}
	if address != strings.ToLower(crypto.PubkeyToAddress(s.key.PublicKey).Hex()) {
		t.Fatalf("address = %q", address)
	}
	if address != strings.ToLower(address) {
		t.Fatalf("address %q is not normalized", address)
	}
}

func TestDevelopmentSignsRecoverableTransaction(t *testing.T) {
	t.Parallel()
	s, err := NewDevelopment("0x" + strings.Repeat("00", 31) + "01")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	signed, err := s.SignTransaction(context.Background(), testTransaction())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	var tx types.Transaction
	if err := tx.UnmarshalBinary(signed.Raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if tx.Type() != types.DynamicFeeTxType {
		t.Fatalf("type = %d, want EIP-1559", tx.Type())
	}
	if tx.Nonce() != 7 || tx.Gas() != 100000 || tx.Value().Sign() != 0 {
		t.Fatalf("tx = nonce %d gas %d value %s", tx.Nonce(), tx.Gas(), tx.Value())
	}
	if tx.GasFeeCap().String() != "30000000000" || tx.GasTipCap().String() != "2000000000" {
		t.Fatalf("fees = %s / %s", tx.GasFeeCap(), tx.GasTipCap())
	}
	from, err := types.Sender(types.LatestSignerForChainID(big.NewInt(1)), &tx)
	if err != nil {
		t.Fatalf("recover sender: %v", err)
	}
	if from != crypto.PubkeyToAddress(s.key.PublicKey) {
		t.Fatalf("sender = %s", from)
	}
	if *tx.To() != common.HexToAddress(testTransaction().To) {
		t.Fatalf("to = %s", tx.To())
	}
}

func TestDevelopmentRejectsUnsafeTransaction(t *testing.T) {
	t.Parallel()
	s, err := NewDevelopment("0x" + strings.Repeat("00", 31) + "01")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	tx := testTransaction()
	tx.Value = "1"
	if _, err := s.SignTransaction(context.Background(), tx); err == nil {
		t.Fatal("expected non-zero value to be rejected")
	}
	tx = testTransaction()
	tx.GasLimit = 0
	if _, err := s.SignTransaction(context.Background(), tx); err == nil {
		t.Fatal("expected zero gas limit to be rejected")
	}
}
