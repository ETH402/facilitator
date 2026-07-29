//go:build integration

package signer

import (
	"context"
	"encoding/hex"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"golang.org/x/crypto/sha3"
)

// TestCloudKMSSettlementEndToEnd proves the production signer path against a
// real Cloud KMS key and the local Anvil chain: address resolution, signing,
// and a broadcast that the chain accepts. Anvil runs with chain id 1 and
// instant mining; the well-known Anvil development key funds the KMS address
// (it is a public local fixture, never a funded mainnet key). Set
// ETH402_TEST_KMS_KEY_NAME to the full key version resource to run it.
func TestCloudKMSSettlementEndToEnd(t *testing.T) {
	keyName := os.Getenv("ETH402_TEST_KMS_KEY_NAME")
	if keyName == "" {
		t.Skip("ETH402_TEST_KMS_KEY_NAME is not set")
	}
	anvilURL := os.Getenv("ETH402_TEST_ANVIL_URL")
	if anvilURL == "" {
		anvilURL = "http://localhost:8545"
	}
	ctx := context.Background()
	rpc := ethereum.NewClient(anvilURL, "", 5*time.Second, 1)

	kmsClient, err := NewCloudKMSClient(ctx)
	if err != nil {
		t.Fatalf("KMS client: %v", err)
	}
	defer func() { _ = kmsClient.Close() }()
	backend, err := NewCloudKMS(ctx, kmsClient, keyName)
	if err != nil {
		t.Fatalf("KMS signer: %v", err)
	}
	address, err := backend.Address(ctx)
	if err != nil {
		t.Fatalf("address: %v", err)
	}
	t.Logf("KMS signer address: %s", address)

	fundOnAnvil(t, rpc, address, "10000000000000000") // 0.01 ETH covers 100k gas at 30 gwei

	nonce, err := rpc.TransactionCount(ctx, address)
	if err != nil {
		t.Fatalf("signer nonce: %v", err)
	}
	signed, err := backend.SignTransaction(ctx, Transaction{
		ChainID: 1, Nonce: nonce,
		To:    "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
		Data:  settlementCalldata(), // selector-shaped dummy calldata
		Value: "0", GasLimit: 100000,
		MaxFeePerGas: "30000000000", MaxPriorityFeePerGas: "2000000000",
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	parsed := new(types.Transaction)
	if err := parsed.UnmarshalBinary(signed.Raw); err != nil {
		t.Fatalf("unmarshal signed: %v", err)
	}
	from, err := types.Sender(types.LatestSignerForChainID(big.NewInt(1)), parsed)
	if err != nil {
		t.Fatalf("recover sender: %v", err)
	}
	if got := from.Hex(); got != common.HexToAddress(address).Hex() {
		t.Fatalf("sender = %s, want %s", got, address)
	}

	txHash, err := rpc.SendRawTransaction(ctx, "0x"+common.Bytes2Hex(signed.Raw))
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	receipt := awaitReceipt(t, rpc, txHash)
	if receipt.Status != 1 {
		t.Fatalf("KMS-signed transaction reverted: %+v", receipt)
	}
}

// fundOnAnvil sends ether from the first Anvil development account. Anvil's
// default key is public knowledge and exists only on the local chain.
func fundOnAnvil(t *testing.T, rpc *ethereum.Client, to, wei string) {
	t.Helper()
	ctx := context.Background()
	const anvilDevKey = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	key, err := crypto.HexToECDSA(anvilDevKey)
	if err != nil {
		t.Fatal(err)
	}
	from := crypto.PubkeyToAddress(key.PublicKey)
	nonce, err := rpc.TransactionCount(ctx, from.Hex())
	if err != nil {
		t.Fatalf("funding nonce: %v", err)
	}
	amount, _ := new(big.Int).SetString(wei, 10)
	recipient := common.HexToAddress(to)
	unsigned := types.NewTx(&types.DynamicFeeTx{
		ChainID: big.NewInt(1), Nonce: nonce, To: &recipient, Value: amount,
		Gas: 21000, GasFeeCap: big.NewInt(30000000000), GasTipCap: big.NewInt(2000000000),
	})
	signed, err := types.SignTx(unsigned, types.LatestSignerForChainID(big.NewInt(1)), key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := signed.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	txHash, err := rpc.SendRawTransaction(ctx, "0x"+common.Bytes2Hex(raw))
	if err != nil {
		t.Fatalf("fund: %v", err)
	}
	if receipt := awaitReceipt(t, rpc, txHash); receipt.Status != 1 {
		t.Fatalf("funding transaction reverted: %+v", receipt)
	}
}

func awaitReceipt(t *testing.T, rpc *ethereum.Client, txHash string) *ethereum.Receipt {
	t.Helper()
	for range 20 {
		receipt, err := rpc.TransactionReceipt(context.Background(), txHash)
		if err != nil {
			t.Fatalf("receipt: %v", err)
		}
		if receipt != nil {
			return receipt
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("transaction %s never mined", txHash)
	return nil
}

// TestCloudKMSSigningIsDeterministic decides whether ADR-0004's ambiguous
// broadcast recovery actually works with the production signer.
//
// settlement.signIdentical re-signs a stored transaction after an ambiguous
// broadcast and refuses to send unless the re-signed bytes hash to the value
// already recorded. That is only possible if signing is reproducible. The
// development signer is deterministic (RFC 6979 via go-ethereum), but Cloud KMS
// makes no such promise and HSM-backed ECDSA commonly uses a random k.
//
// If this fails, no funds are at risk — signIdentical refuses rather than
// broadcasting a different transaction — but ambiguous broadcasts can then only
// ever be resolved by on-chain lookup, and after the grace window they stay in
// manual_review until an operator intervenes. Document it and drop the
// re-broadcast claim rather than leaving the capability advertised.
func TestCloudKMSSigningIsDeterministic(t *testing.T) {
	keyName := os.Getenv("ETH402_TEST_KMS_KEY_NAME")
	if keyName == "" {
		t.Skip("ETH402_TEST_KMS_KEY_NAME is not set")
	}
	ctx := context.Background()
	kmsClient, err := NewCloudKMSClient(ctx)
	if err != nil {
		t.Fatalf("KMS client: %v", err)
	}
	defer func() { _ = kmsClient.Close() }()
	backend, err := NewCloudKMS(ctx, kmsClient, keyName)
	if err != nil {
		t.Fatalf("KMS signer: %v", err)
	}
	tx := Transaction{
		ChainID: 1, Nonce: 11,
		To:    "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
		Data:  settlementCalldata(),
		Value: "0", GasLimit: 120000,
		MaxFeePerGas: "30000000000", MaxPriorityFeePerGas: "2000000000",
	}
	hashes := make([]string, 0, 3)
	for range 3 {
		signed, err := backend.SignTransaction(ctx, tx)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		keccak := sha3.NewLegacyKeccak256()
		keccak.Write(signed.Raw)
		hashes = append(hashes, hex.EncodeToString(keccak.Sum(nil)))
	}
	t.Logf("Cloud KMS signed-transaction hashes: %v", hashes)
	for i, hash := range hashes {
		if hash != hashes[0] {
			t.Fatalf("Cloud KMS signing is NOT deterministic (attempt %d hashed %s, first %s).\n"+
				"settlement.signIdentical can therefore never reproduce a stored transaction, so the "+
				"ambiguous re-broadcast path in ADR-0004 decision 4 is unreachable with this backend. "+
				"Recovery degrades to on-chain lookup only; update the ADR, SETTLEMENT_FLOW.md, and "+
				"OPERATIONS.md rather than leaving the capability documented.", i, hash, hashes[0])
		}
	}
}
