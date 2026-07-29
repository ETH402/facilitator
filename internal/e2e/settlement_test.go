//go:build e2e

// Package e2e drives the facilitator against a real USDC contract.
//
// Every other test stubs the chain: settlement uses fake signers and fake
// receipts, and the KMS test sends selector-shaped bytes to an address with no
// code. Nothing has ever executed a real EIP-3009 transferWithAuthorization, so
// nothing has proven the facilitator moves money. This does.
//
// It needs a mainnet-forked Anvil, which supplies genuine USDC at the canonical
// address with the exact EIP-712 domain the verifier demands:
//
//	docker run -d --name eth402-fork -p 8546:8545 --entrypoint anvil \
//	  ghcr.io/foundry-rs/foundry:stable \
//	  --host 0.0.0.0 --fork-url https://ethereum-rpc.publicnode.com --chain-id 1
//
//	ETH402_TEST_FORK_RPC_URL=http://localhost:8546 \
//	ETH402_TEST_DATABASE_URL=postgres://eth402:eth402_dev_only@localhost:5432/eth402_test?sslmode=disable \
//	  go test -tags=e2e -count=1 -v ./internal/e2e
//
// The build tag keeps it out of CI, which has no fork to point at.
package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/config"
	"github.com/ETH402/facilitator/internal/ethereum"
	"github.com/ETH402/facilitator/internal/httpapi"
	"github.com/ETH402/facilitator/internal/metrics"
	"github.com/ETH402/facilitator/internal/migrate"
	"github.com/ETH402/facilitator/internal/settlement"
	"github.com/ETH402/facilitator/internal/signer"
	"github.com/ETH402/facilitator/internal/stats"
	"github.com/ETH402/facilitator/internal/store"
	"github.com/ETH402/facilitator/internal/verification"
	"github.com/ETH402/facilitator/migrations"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5"
	x402evm "github.com/x402-foundation/x402/go/v2/mechanisms/evm"
	exactfacilitator "github.com/x402-foundation/x402/go/v2/mechanisms/evm/exact/facilitator"
	"github.com/x402-foundation/x402/go/v2/types"
)

const usdcABI = `[
{"inputs":[{"name":"account","type":"address"}],"name":"balanceOf","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
{"inputs":[],"name":"masterMinter","outputs":[{"name":"","type":"address"}],"stateMutability":"view","type":"function"},
{"inputs":[{"name":"minter","type":"address"},{"name":"allowance","type":"uint256"}],"name":"configureMinter","outputs":[{"name":"","type":"bool"}],"stateMutability":"nonpayable","type":"function"},
{"inputs":[{"name":"to","type":"address"},{"name":"amount","type":"uint256"}],"name":"mint","outputs":[{"name":"","type":"bool"}],"stateMutability":"nonpayable","type":"function"}]`

// rpc is a minimal JSON-RPC caller. The production client deliberately exposes
// only the narrow set of methods settlement needs, and this test needs Anvil's
// state-manipulation methods, which have no business being on it.
type rpc struct {
	t   *testing.T
	url string
}

func (r rpc) call(method string, params ...any) json.RawMessage {
	r.t.Helper()
	if params == nil {
		params = []any{}
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	response, err := http.Post(r.url, "application/json", bytes.NewReader(body))
	if err != nil {
		r.t.Fatalf("%s: %v", method, err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		r.t.Fatal(err)
	}
	var decoded struct {
		Result json.RawMessage           `json:"result"`
		Error  *struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		r.t.Fatalf("%s: %s", method, raw)
	}
	if decoded.Error != nil {
		// A forked Anvil pins the block it started from, and public endpoints serve
		// only a short window of historical state. Once the chain moves past that
		// window the fork can no longer read the state it needs, and the upstream
		// error names archive access rather than anything about this test — which
		// reads like a permissions problem in the facilitator. Say what it is.
		if strings.Contains(decoded.Error.Message, "Archive request") ||
			strings.Contains(decoded.Error.Message, "archive") {
			r.t.Fatalf("%s failed because the mainnet fork has aged out of its upstream's "+
				"historical window; restart it and rerun:\n"+
				"  docker rm -f eth402-fork && docker run -d --name eth402-fork -p 8546:8545 \\\n"+
				"    --entrypoint anvil ghcr.io/foundry-rs/foundry:stable \\\n"+
				"    --host 0.0.0.0 --fork-url https://ethereum-rpc.publicnode.com --chain-id 1\n"+
				"upstream said: %s", method, decoded.Error.Message)
		}
		r.t.Fatalf("%s: %s", method, decoded.Error.Message)
	}
	return decoded.Result
}

// send submits a transaction from an impersonated account and waits for success.
func (r rpc) send(from, to string, data []byte) {
	r.t.Helper()
	var hash string
	result := r.call("eth_sendTransaction", map[string]string{
		"from": from, "to": to, "data": "0x" + common.Bytes2Hex(data), "gas": "0x100000",
	})
	if err := json.Unmarshal(result, &hash); err != nil {
		r.t.Fatal(err)
	}
	for range 40 {
		receipt := r.call("eth_getTransactionReceipt", hash)
		if string(receipt) != "null" {
			var parsed struct{ Status string }
			if err := json.Unmarshal(receipt, &parsed); err != nil {
				r.t.Fatal(err)
			}
			if parsed.Status != "0x1" {
				r.t.Fatalf("setup transaction %s failed with status %s", hash, parsed.Status)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	r.t.Fatalf("setup transaction %s never mined", hash)
}

func TestFacilitatorMovesRealUSDC(t *testing.T) {
	forkURL := os.Getenv("ETH402_TEST_FORK_RPC_URL")
	databaseURL := os.Getenv("ETH402_TEST_DATABASE_URL")
	if forkURL == "" || databaseURL == "" {
		t.Skip("set ETH402_TEST_FORK_RPC_URL and ETH402_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	chain := rpc{t: t, url: forkURL}
	usdc, err := abi.JSON(strings.NewReader(usdcABI))
	if err != nil {
		t.Fatal(err)
	}
	client := ethereum.NewClient(forkURL, "", 20*time.Second, 1)

	// The fork must be mainnet with the real token, or the verifier's domain
	// checks are meaningless.
	if chainID, err := client.ChainID(ctx); err != nil || chainID != 1 {
		t.Fatalf("fork chain id = %d, %v; want 1", chainID, err)
	}

	// --- participants -------------------------------------------------------
	buyerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	buyer := crypto.PubkeyToAddress(buyerKey.PublicKey)
	signerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signerAddress := crypto.PubkeyToAddress(signerKey.PublicKey)
	// A fresh recipient per run: the fork keeps its state between runs, so a fixed
	// address accumulates balances and the assertions below would drift.
	recipientKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	merchantRecipient := crypto.PubkeyToAddress(recipientKey.PublicKey)

	// The facilitator pays gas, so its signer needs ether. 1 ETH is ample.
	chain.call("anvil_setBalance", signerAddress.Hex(), "0xde0b6b3a7640000")

	// Give the buyer USDC through the token's own minting path rather than by
	// writing storage slots, so the balance is real state the contract agrees with.
	var masterMinter common.Address
	{
		var raw string
		result := chain.call("eth_call", map[string]string{
			"to": config.MainnetUSDC, "data": "0x" + common.Bytes2Hex(mustPack(t, usdc, "masterMinter")),
		}, "latest")
		if err := json.Unmarshal(result, &raw); err != nil {
			t.Fatal(err)
		}
		masterMinter = common.HexToAddress(raw)
	}
	chain.call("anvil_impersonateAccount", masterMinter.Hex())
	chain.call("anvil_setBalance", masterMinter.Hex(), "0xde0b6b3a7640000")
	const minted = 500_000_000 // 500 USDC at 6 decimals
	chain.send(masterMinter.Hex(), config.MainnetUSDC,
		mustPack(t, usdc, "configureMinter", masterMinter, big.NewInt(minted*2)))
	chain.send(masterMinter.Hex(), config.MainnetUSDC,
		mustPack(t, usdc, "mint", buyer, big.NewInt(minted)))
	if got := balanceOf(t, chain, usdc, buyer); got.Int64() != minted {
		t.Fatalf("buyer USDC = %s, want %d", got, minted)
	}

	// --- the facilitator ----------------------------------------------------
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, conn, migrations.Files); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(ctx); err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(ctx, databaseURL, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Pool.Exec(ctx, "TRUNCATE merchants, payment_records CASCADE"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx,
		`DELETE FROM signer_accounts WHERE signer_address = $1`, strings.ToLower(signerAddress.Hex())); err != nil {
		t.Fatal(err)
	}
	// /settle admits only payments whose recipient is an active registered
	// merchant (ADR-0004 decision 9), so the recipient must exist as one.
	if _, err := database.Pool.Exec(ctx, `INSERT INTO merchants
		(name,business_email,email_domain,recipient_address,terms_version,terms_accepted_at,status,email_verified_at,wallet_verified_at)
		VALUES ('E2E','e2e@example.com','example.com',$1,'v1',now(),'active',now(),now())`,
		strings.ToLower(merchantRecipient.Hex())); err != nil {
		t.Fatal(err)
	}
	chainNonce, err := client.TransactionCount(ctx, signerAddress.Hex())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SeedSignerAccount(ctx, database.Pool, signerAddress.Hex(), chainNonce); err != nil {
		t.Fatal(err)
	}

	verificationRPC, err := ethereum.NewVerificationClient(forkURL, "")
	if err != nil {
		t.Fatal(err)
	}
	defer verificationRPC.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	devSigner, err := signer.NewDevelopment(common.Bytes2Hex(crypto.FromECDSA(signerKey)))
	if err != nil {
		t.Fatal(err)
	}
	settlementService := settlement.NewService(database, devSigner, client, settlement.Config{
		SignerAddress: strings.ToLower(signerAddress.Hex()),
		ExpiryMargin:  time.Minute, SigningTimeout: 20 * time.Second,
		LeaseDuration: 2 * time.Minute, WorkerInterval: time.Second,
		Confirmations: 1, GasLimit: 250_000,
		MaxFeePerGas: "500000000000", MaxPriorityFeeGas: "2000000000",
		RecoveryGrace: 2 * time.Minute, ReplacementAfter: 5 * time.Minute,
		MerchantQuota: 100, GlobalQuota: 10_000, QuotaWindow: 24 * time.Hour,
	}, logger)
	api := httpapi.New(httpapi.Dependencies{
		Logger: logger, Database: database, Ethereum: client,
		Stats: stats.NewService(stats.Config{
			Source: database, Started: time.Now(),
			Health: stats.NewAssessor(stats.AssessorConfig{
				Database: database, Chain: client, ExpectedChainID: 1,
				ExpectedWorkers: []string{"broadcast", "confirmation", "recovery"},
				StaleAfter:      time.Minute, SettlementEnabled: true,
			}),
		}), Metrics: metrics.New(),
		ExpectedChainID: 1, PublicRatePerMinute: 1000, RegistrationRate: 100,
		MetricsEnabled: true,
		Verification: verification.New(
			exactfacilitator.NewExactEvmScheme(verificationRPC, nil),
			verificationRPC, database, 20*time.Second),
		Settlement: settlementService,
	}).Handler()
	server := httptest.NewServer(api)
	defer server.Close()

	// --- the payment --------------------------------------------------------
	const amount = "1500000" // 1.50 USDC
	request := signedRequest(t, buyerKey, buyer, merchantRecipient, amount)

	var verifyResponse struct {
		IsValid       bool   `json:"isValid"`
		InvalidReason string `json:"invalidReason"`
	}
	postJSON(t, server.URL+"/verify", request, &verifyResponse)
	if !verifyResponse.IsValid {
		t.Fatalf("/verify rejected a genuine payment: %s", verifyResponse.InvalidReason)
	}

	var settleResponse struct {
		Success     bool   `json:"success"`
		Transaction string `json:"transaction"`
		ErrorReason string `json:"errorReason"`
	}
	postJSON(t, server.URL+"/settle", request, &settleResponse)
	if !settleResponse.Success {
		t.Fatalf("/settle failed: %s", settleResponse.ErrorReason)
	}
	if settleResponse.Transaction == "" {
		t.Fatal("/settle reported success with no transaction hash")
	}
	t.Logf("settled in %s", settleResponse.Transaction)

	// --- the money actually moved -------------------------------------------
	receipt := awaitReceipt(t, client, settleResponse.Transaction)
	if receipt.Status != 1 {
		t.Fatalf("settlement transaction reverted: %+v", receipt)
	}
	want := big.NewInt(1_500_000)
	if got := balanceOf(t, chain, usdc, merchantRecipient); got.Cmp(want) != 0 {
		t.Fatalf("merchant USDC = %s, want %s", got, want)
	}
	remaining := big.NewInt(minted)
	remaining.Sub(remaining, want)
	if got := balanceOf(t, chain, usdc, buyer); got.Cmp(remaining) != 0 {
		t.Fatalf("buyer USDC = %s, want %s", got, remaining)
	}

	// The authorization nonce must now be spent on chain, which is what stops a
	// replay independently of anything this service records.
	if !authorizationUsed(t, chain, buyer, request) {
		t.Fatal("USDC does not report the authorization nonce as used")
	}

	// A duplicate settle must converge on the recorded transaction rather than
	// paying gas twice.
	var duplicate struct {
		Success     bool   `json:"success"`
		Transaction string `json:"transaction"`
	}
	postJSON(t, server.URL+"/settle", request, &duplicate)
	if !duplicate.Success || duplicate.Transaction != settleResponse.Transaction {
		t.Fatalf("duplicate settle = %+v, want the recorded hash", duplicate)
	}

	// Confirmation must advance the payment to confirmed at the configured depth.
	// The schema constrains actor_type to http/worker/operator/system.
	if err := settlementService.Confirmation(ctx, paymentID(t, database, request), "worker"); err != nil {
		t.Fatalf("confirmation: %v", err)
	}
	var state string
	if err := database.Pool.QueryRow(ctx,
		`SELECT state FROM payment_records WHERE payment_identity = $1`,
		identityOf(t, request)).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "confirmed" && state != "confirming" {
		t.Fatalf("payment state = %q, want confirmed or confirming", state)
	}
	t.Logf("payment state after confirmation: %s", state)
}

func mustPack(t *testing.T, parsed abi.ABI, method string, args ...any) []byte {
	t.Helper()
	data, err := parsed.Pack(method, args...)
	if err != nil {
		t.Fatalf("pack %s: %v", method, err)
	}
	return data
}

func balanceOf(t *testing.T, chain rpc, parsed abi.ABI, who common.Address) *big.Int {
	t.Helper()
	var raw string
	result := chain.call("eth_call", map[string]string{
		"to": config.MainnetUSDC, "data": "0x" + common.Bytes2Hex(mustPack(t, parsed, "balanceOf", who)),
	}, "latest")
	if err := json.Unmarshal(result, &raw); err != nil {
		t.Fatal(err)
	}
	value, ok := new(big.Int).SetString(strings.TrimPrefix(raw, "0x"), 16)
	if !ok {
		t.Fatalf("balanceOf returned %q", raw)
	}
	return value
}

func authorizationUsed(t *testing.T, chain rpc, payer common.Address, request verification.Request) bool {
	t.Helper()
	authorization := request.PaymentPayload.Payload["authorization"].(map[string]any)
	nonce := common.HexToHash(authorization["nonce"].(string))
	parsed, err := abi.JSON(strings.NewReader(string(x402evm.AuthorizationStateABI)))
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	result := chain.call("eth_call", map[string]string{
		"to": config.MainnetUSDC,
		"data": "0x" + common.Bytes2Hex(mustPack(t, parsed,
			x402evm.FunctionAuthorizationState, payer, nonce)),
	}, "latest")
	if err := json.Unmarshal(result, &raw); err != nil {
		t.Fatal(err)
	}
	return strings.HasSuffix(raw, "1")
}

func postJSON(t *testing.T, url string, payload, into any) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("%s returned %d %s", url, response.StatusCode, raw)
	}
}

func awaitReceipt(t *testing.T, client *ethereum.Client, hash string) *ethereum.Receipt {
	t.Helper()
	for range 60 {
		receipt, err := client.TransactionReceipt(context.Background(), hash)
		if err != nil {
			t.Fatal(err)
		}
		if receipt != nil {
			return receipt
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("transaction %s never mined", hash)
	return nil
}

// signedRequest builds a genuine x402 v2 exact payment, signed by the buyer over
// USDC's real EIP-712 domain.
func signedRequest(t *testing.T, key *ecdsa.PrivateKey, buyer, recipient common.Address, amount string) verification.Request {
	t.Helper()
	requirements := types.PaymentRequirements{
		Scheme: "exact", Network: config.MainnetNetwork, Asset: config.MainnetUSDC,
		Amount: amount, PayTo: recipient.Hex(), MaxTimeoutSeconds: 300,
		Extra: map[string]any{
			"name": verification.USDCName, "version": verification.USDCVersion,
			"assetTransferMethod": "eip3009",
		},
	}
	authorization := map[string]any{
		"from": buyer.Hex(), "to": recipient.Hex(), "value": amount,
		"validAfter":  "0",
		"validBefore": fmt.Sprintf("%d", time.Now().Add(time.Hour).Unix()),
		"nonce":       "0x" + common.Bytes2Hex(crypto.Keccak256([]byte(time.Now().String()))),
	}
	digest, err := x402evm.HashEIP3009Authorization(
		x402evm.ExactEIP3009Authorization{
			From: authorization["from"].(string), To: authorization["to"].(string),
			Value: authorization["value"].(string), ValidAfter: authorization["validAfter"].(string),
			ValidBefore: authorization["validBefore"].(string), Nonce: authorization["nonce"].(string),
		},
		big.NewInt(1), common.HexToAddress(config.MainnetUSDC).Hex(),
		verification.USDCName, verification.USDCVersion)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := crypto.Sign(digest, key)
	if err != nil {
		t.Fatal(err)
	}
	// Left as crypto.Sign emits it, recovery id 0 or 1: exactly what a wallet
	// library sends, and what settlement must normalize rather than reject.
	return verification.Request{
		X402Version: 2,
		PaymentPayload: types.PaymentPayload{
			X402Version: 2, Accepted: requirements,
			Payload: map[string]any{
				"signature": "0x" + common.Bytes2Hex(signature), "authorization": authorization,
			},
		},
		PaymentRequirements: requirements,
	}
}

func identityOf(t *testing.T, request verification.Request) string {
	t.Helper()
	payment, reason := verification.ParseRequest(request)
	if reason != "" {
		t.Fatalf("built an invalid request: %s", reason)
	}
	return payment.Identity
}

func paymentID(t *testing.T, database *store.Store, request verification.Request) string {
	t.Helper()
	var id string
	if err := database.Pool.QueryRow(context.Background(),
		`SELECT id FROM payment_records WHERE payment_identity = $1`, identityOf(t, request)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
