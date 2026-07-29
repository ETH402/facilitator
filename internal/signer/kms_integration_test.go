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

// TestCloudKMSSigHashStableAcrossSignatures proves the foundation of
// ambiguous-broadcast recovery against the real production key: the sighash is
// identical for every signature of the same transaction, while the signed
// bytes differ because Cloud KMS randomizes the ECDSA nonce.
//
// Recovery relies on exactly this split. settlement.signIdentical re-signs a
// stored transaction after an ambiguous broadcast and proves identity by the
// stored sighash; the fresh signature's different hash is then recorded
// replacement-shaped (ADR-0004 decision 4). If KMS ever produced a stable raw
// hash that would also be fine — recovery takes the identical-bytes path — but
// a sighash that varied for identical inputs would break recovery and fails
// here. Set ETH402_TEST_KMS_KEY_NAME to the full key version resource to run.
func TestCloudKMSSigHashStableAcrossSignatures(t *testing.T) {
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
	rawHashes := make([]string, 0, 3)
	var sighash [32]byte
	for i := range 3 {
		signed, err := backend.SignTransaction(ctx, tx)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if i == 0 {
			sighash = signed.SigHash
		} else if signed.SigHash != sighash {
			t.Fatalf("sighash is not stable across signatures: attempt %d gave %x, first %x",
				i, signed.SigHash, sighash)
		}
		keccak := sha3.NewLegacyKeccak256()
		keccak.Write(signed.Raw)
		rawHashes = append(rawHashes, hex.EncodeToString(keccak.Sum(nil)))
	}
	t.Logf("Cloud KMS sighash %x stable; raw hashes: %v", sighash, rawHashes)
	for i, hash := range rawHashes {
		if hash == rawHashes[0] && i > 0 {
			t.Fatalf("raw hashes are unexpectedly identical (%s); "+
				"Cloud KMS signing appears deterministic, so the randomized-nonce recovery "+
				"path is no longer exercised — re-run or investigate the backend change", hash)
		}
	}
}
