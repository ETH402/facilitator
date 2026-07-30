//go:build integration

package settlement_test

// Drives the stuck-transaction replacement path against a real Anvil mempool
// running with mining disabled. Every other test stubs the chain, so the
// pool's replacement semantics have never been exercised by anything real.
// Here they are:
//
//   - an original broadcast stays pending because nothing mines,
//   - a same-nonce replacement bumped by only 1 wei is accepted by Anvil's
//     pool and evicts the original from it, while the durable record stays
//     exactly as the broadcast left it,
//   - the recovery worker's proper fee bump outbids that squatter and is
//     accepted in turn,
//   - and once evm_mine mines the replacement, confirmation finalizes the
//     payment from the replacement's receipt, whose hash differs from the
//     original broadcast hash.
//
// One deliberate divergence from mainnet: Anvil's pool replaces a pending
// transaction on any strictly greater fee, with no 10% price-bump rule, so a
// geth mempool would reject the 1-wei replacement as underpriced and keep the
// original. The production-side answer to that rule is BumpFees's ≥110%
// floors, which are unit-tested; what this test adds is that recovery's
// computed bump really does outbid whatever squats on the nonce in a live
// pool, and that foreign mempool activity never rewrites the durable record.
//
// It needs the no-mining Anvil from compose.yaml:
//
//	docker compose --profile testing up -d anvil-nomine postgres
//
//	ETH402_TEST_ANVIL_NOMINE_URL=http://localhost:8547 \
//	ETH402_TEST_DATABASE_URL=postgres://eth402:eth402_dev_only@localhost:5432/eth402_test?sslmode=disable \
//	  go test -tags=integration -p 1 -run TestReplacementAgainstRealMempool -v ./internal/settlement/
//
// Plain Anvil has no USDC contract, so the settlement transaction's call into
// the canonical USDC address returns empty success and the transaction mines
// with status 1. That is fine: this test exercises mempool, replacement, and
// confirmation mechanics; USDC semantics are the forked e2e test's job.

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/config"
	"github.com/ETH402/facilitator/internal/ethereum"
	"github.com/ETH402/facilitator/internal/migrate"
	"github.com/ETH402/facilitator/internal/settlement"
	"github.com/ETH402/facilitator/internal/signer"
	"github.com/ETH402/facilitator/internal/store"
	"github.com/ETH402/facilitator/migrations"
	"github.com/jackc/pgx/v5"
)

// Anvil's well-known account #0, genesis-funded, so no funding step is needed.
// Never use this key anywhere else: it is public.
const (
	nomineDevKey     = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	nomineDevAddress = "0xf39fd6e51aad88f6f4ce6ab8827279cfffb92266"
)

// evmMine mines one block on demand. The production client deliberately lacks
// Anvil's mining controls, so the test calls it directly.
func evmMine(t *testing.T, url string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "evm_mine", "params": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("evm_mine: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("evm_mine: %s", raw)
	}
	if decoded.Error != nil {
		t.Fatalf("evm_mine: %s", decoded.Error.Message)
	}
}

// txRow is the durable record of one settlement transaction.
type txRow struct {
	id                string
	status            string
	txHash            string
	maxFeePerGas      string
	maxPriorityFee    string
	ambiguousAttempts int
}

func loadTxRows(t *testing.T, ctx context.Context, database *store.Store, paymentID string) []txRow {
	t.Helper()
	rows, err := database.Pool.Query(ctx, `
SELECT id, status, COALESCE(tx_hash, ''), COALESCE(max_fee_per_gas::text, ''),
       COALESCE(max_priority_fee_per_gas::text, ''), ambiguous_attempts
FROM ethereum_transactions WHERE payment_id = $1 ORDER BY created_at, id`, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []txRow
	for rows.Next() {
		var row txRow
		if err := rows.Scan(&row.id, &row.status, &row.txHash,
			&row.maxFeePerGas, &row.maxPriorityFee, &row.ambiguousAttempts); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func paymentState(t *testing.T, ctx context.Context, database *store.Store, paymentID string) string {
	t.Helper()
	var state string
	if err := database.Pool.QueryRow(ctx,
		`SELECT state FROM payment_records WHERE id = $1`, paymentID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// awaitPendingMempool polls until hash is visible in the mempool (known but
// not mined), which is how a real provider reports a queued transaction.
func awaitPendingMempool(t *testing.T, ctx context.Context, client *ethereum.Client, hash string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		known, err := client.TransactionByHash(ctx, hash)
		if err != nil {
			t.Fatalf("fetch %s: %v", hash, err)
		}
		if known != nil {
			if known.BlockNumber != nil {
				t.Fatalf("transaction %s mined without evm_mine; is Anvil running --no-mining?", hash)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("transaction %s never appeared in the mempool", hash)
}

func TestReplacementAgainstRealMempool(t *testing.T) {
	rpcURL := os.Getenv("ETH402_TEST_ANVIL_NOMINE_URL")
	databaseURL := os.Getenv("ETH402_TEST_DATABASE_URL")
	if rpcURL == "" || databaseURL == "" {
		t.Skip("set ETH402_TEST_ANVIL_NOMINE_URL and ETH402_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	client := ethereum.NewClient(rpcURL, "", 10*time.Second, 1)
	if chainID, err := client.ChainID(ctx); err != nil || chainID != 1 {
		t.Fatalf("no-mining anvil chain id = %d, %v; want 1", chainID, err)
	}

	// Flush any pending transactions left by an interrupted prior run: the
	// container may keep its mempool, and a leftover on the signer's next
	// nonce would collide with this run's broadcast.
	evmMine(t, rpcURL)

	// --- database -----------------------------------------------------------
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
		`DELETE FROM signer_accounts WHERE signer_address = $1`, nomineDevAddress); err != nil {
		t.Fatal(err)
	}
	chainNonce, err := client.TransactionCount(ctx, nomineDevAddress)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SeedSignerAccount(ctx, database.Pool, nomineDevAddress, chainNonce); err != nil {
		t.Fatal(err)
	}

	// --- a verified payment with a committed intent -------------------------
	var merchantID string
	if err := database.Pool.QueryRow(ctx, `INSERT INTO merchants
		(name,business_email,email_domain,recipient_address,terms_version,terms_accepted_at,status,email_verified_at,wallet_verified_at)
		VALUES ('NoMine','nomine@example.com','example.com','0x1111111111111111111111111111111111111111','v1',now(),'active',now(),now())
		RETURNING id`).Scan(&merchantID); err != nil {
		t.Fatal(err)
	}
	identity := "pay_" + strings.Repeat("7", 64)
	var paymentID string
	if err := database.Pool.QueryRow(ctx, `INSERT INTO payment_records
		(payment_identity,merchant_id,x402_version,scheme,network,asset,payer_address,recipient_address,
		 amount_atomic,authorization_nonce,valid_after,valid_before,payload_hash,verification_status,state)
		VALUES ($1,$2,2,'exact','eip155:1','0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48',
		        '0x3333333333333333333333333333333333333333','0x1111111111111111111111111111111111111111',
		        42,$3,now()-interval '1 minute',now()+interval '1 hour',$4,'verified','verified')
		RETURNING id`,
		identity, merchantID, "0x"+strings.Repeat("8", 64), strings.Repeat("9", 64)).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateSettlementIntent(ctx, settlement.IntentRequest{
		PaymentIdentity: identity, SignerAddress: nomineDevAddress,
		PayerSignature: "0x" + strings.Repeat("1", 64) + strings.Repeat("2", 64) + "1b",
		ExpiryMargin:   time.Minute, Now: time.Now(),
		Quota: 100, GlobalQuota: 10_000, QuotaWindow: 24 * time.Hour,
	}); err != nil {
		t.Fatal(err)
	}

	devSigner, err := signer.NewDevelopment(nomineDevKey)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := settlement.NewService(database, devSigner, client, settlement.Config{
		SignerAddress: nomineDevAddress,
		ExpiryMargin:  time.Minute, SigningTimeout: 20 * time.Second,
		LeaseDuration: 2 * time.Minute, WorkerInterval: 100 * time.Millisecond,
		Confirmations: 1, GasLimit: 250_000,
		MaxFeePerGas: "500000000000", MaxPriorityFeeGas: "2000000000",
		RecoveryGrace: 2 * time.Minute, ReplacementAfter: 5 * time.Minute,
		MerchantQuota: 100, GlobalQuota: 10_000, QuotaWindow: 24 * time.Hour,
	}, logger)

	// --- (a) the original broadcast sits pending -----------------------------
	originalHash, err := service.Broadcast(ctx, paymentID, "worker")
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	if originalHash == "" {
		t.Fatal("broadcast returned no transaction hash")
	}
	awaitPendingMempool(t, ctx, client, originalHash)
	if receipt, err := client.TransactionReceipt(ctx, originalHash); err != nil || receipt != nil {
		t.Fatalf("original receipt = %+v, %v; want nil while --no-mining", receipt, err)
	}
	if state := paymentState(t, ctx, database, paymentID); state != "broadcast" {
		t.Fatalf("payment state = %q, want broadcast", state)
	}

	// --- (b) a 1-wei bump replaces in Anvil's pool, untouched record ---------
	// BumpFees refuses an under-110% bump by design, so this goes through the
	// signer and the raw send path directly. A geth mempool would reject the
	// same-nonce replacement as underpriced; Anvil's pool applies no price-bump
	// rule and evicts the original for any strictly greater fee. Either way the
	// durable record must stay exactly as the broadcast left it: the foreign
	// transaction never went through this service.
	work, err := database.LoadSettlementWork(ctx, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	oldMax, _ := new(big.Int).SetString(work.MaxFeePerGas, 10)
	oldTip, _ := new(big.Int).SetString(work.MaxPriorityFeePerGas, 10)
	calldata, err := settlement.TransferWithAuthorizationData(work.Authorization)
	if err != nil {
		t.Fatal(err)
	}
	wire := work.Authorization.Wire()
	underSigned, err := devSigner.SignTransaction(ctx, signer.Transaction{
		ChainID:              config.MainnetChainID,
		Nonce:                work.Nonce,
		To:                   config.MainnetUSDC,
		Data:                 calldata,
		Value:                "0",
		GasLimit:             work.GasLimit,
		MaxFeePerGas:         new(big.Int).Add(oldMax, big.NewInt(1)).String(),
		MaxPriorityFeePerGas: new(big.Int).Add(oldTip, big.NewInt(1)).String(),
		Authorization:        &wire,
	})
	if err != nil {
		t.Fatal(err)
	}
	underHash, err := client.SendRawTransaction(ctx, "0x"+hex.EncodeToString(underSigned.Raw))
	if err != nil {
		// A provider enforcing the price-bump rule would reject here with
		// "underpriced". This test is pinned to anvil-nomine, whose pool
		// accepts; a rejection means the environment changed under it.
		t.Fatalf("under-bumped replacement send: %v", err)
	}
	if underHash == originalHash {
		t.Fatal("the under-bumped replacement reuses the original's hash")
	}
	awaitPendingMempool(t, ctx, client, underHash)
	// The eviction must be real, not a queue: the original hash no longer
	// resolves anywhere on the node.
	if original, err := client.TransactionByHash(ctx, originalHash); err != nil || original != nil {
		t.Fatalf("original after under-bump = %+v, %v; want evicted from the pool", original, err)
	}

	// The durable record is undisturbed: same hash, same status, and no
	// ambiguity accounting — nothing about this send was ever unknown to the
	// service, because the service never made it.
	if state := paymentState(t, ctx, database, paymentID); state != "broadcast" {
		t.Fatalf("payment state after under-bump = %q, want broadcast", state)
	}
	rows := loadTxRows(t, ctx, database, paymentID)
	if len(rows) != 1 || rows[0].status != "broadcast" || rows[0].txHash != originalHash {
		t.Fatalf("transactions after under-bump = %+v; want the one broadcast original", rows)
	}
	if rows[0].ambiguousAttempts != 0 {
		t.Fatalf("ambiguous_attempts = %d; a foreign replacement is not an ambiguous retry", rows[0].ambiguousAttempts)
	}

	// --- (c) the recovery worker's proper bump is accepted -------------------
	// Age the broadcast past ReplacementAfter and let the real worker do the
	// replacement end to end.
	if _, err := database.Pool.Exec(ctx, `
UPDATE ethereum_transactions SET broadcast_attempted_at = now() - interval '10 minutes'
WHERE payment_id = $1 AND status = 'broadcast'`, paymentID); err != nil {
		t.Fatal(err)
	}
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	go service.RecoveryWorker().Run(workerCtx)

	var original, replacement txRow
	deadline := time.Now().Add(20 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("recovery never replaced the stuck transaction; rows: %+v",
				loadTxRows(t, ctx, database, paymentID))
		}
		rows = loadTxRows(t, ctx, database, paymentID)
		if len(rows) == 2 && paymentState(t, ctx, database, paymentID) == "replaced" {
			for _, row := range rows {
				switch row.status {
				case "replaced":
					original = row
				case "broadcast":
					replacement = row
				}
			}
			if original.txHash != "" && replacement.txHash != "" {
				// The row is recorded before the send, so wait for the real
				// mempool to hold the replacement before standing the worker
				// down: cancelling first could abort that send.
				known, err := client.TransactionByHash(ctx, replacement.txHash)
				if err != nil {
					t.Fatalf("fetch replacement: %v", err)
				}
				if known != nil {
					break
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	stopWorker()

	if original.txHash != originalHash {
		t.Fatalf("replaced row hash = %s, want the original %s", original.txHash, originalHash)
	}
	if replacement.txHash == originalHash || replacement.txHash == underHash {
		t.Fatalf("the replacement reuses an existing hash: %s", replacement.txHash)
	}
	if replacement.ambiguousAttempts != 0 || original.ambiguousAttempts != 0 {
		t.Fatalf("ambiguous_attempts = %d/%d; fee bumps are not ambiguous retries",
			original.ambiguousAttempts, replacement.ambiguousAttempts)
	}

	// The accepted bump must satisfy the rule the 1-wei attempt violated.
	newMax, _ := new(big.Int).SetString(replacement.maxFeePerGas, 10)
	newTip, _ := new(big.Int).SetString(replacement.maxPriorityFee, 10)
	ten, eleven := big.NewInt(10), big.NewInt(11)
	if new(big.Int).Mul(newMax, ten).Cmp(new(big.Int).Mul(oldMax, eleven)) < 0 {
		t.Fatalf("replacement max fee %s is under 110%% of %s", newMax, oldMax)
	}
	if new(big.Int).Mul(newTip, ten).Cmp(new(big.Int).Mul(oldTip, eleven)) < 0 {
		t.Fatalf("replacement priority fee %s is under 110%% of %s", newTip, oldTip)
	}

	// The bump is computed from the stored fees, so it strictly outbids the
	// 1-wei squatter too: the pool must now hold the replacement and nothing
	// else on this nonce.
	awaitPendingMempool(t, ctx, client, replacement.txHash)
	if squatter, err := client.TransactionByHash(ctx, underHash); err != nil || squatter != nil {
		t.Fatalf("under-bumped squatter after recovery bump = %+v, %v; want evicted", squatter, err)
	}

	// --- (d) mining the replacement finalizes from its receipt ---------------
	evmMine(t, rpcURL)
	receiptDeadline := time.Now().Add(10 * time.Second)
	var receipt *ethereum.Receipt
	for time.Now().Before(receiptDeadline) {
		receipt, err = client.TransactionReceipt(ctx, replacement.txHash)
		if err != nil {
			t.Fatalf("fetch replacement receipt: %v", err)
		}
		if receipt != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if receipt == nil {
		t.Fatal("the replacement has no receipt after evm_mine")
	}
	if receipt.Status != 1 {
		t.Fatalf("replacement receipt status = %d, want 1", receipt.Status)
	}

	if err := service.Confirmation(ctx, paymentID, "worker"); err != nil {
		t.Fatalf("confirmation: %v", err)
	}
	if state := paymentState(t, ctx, database, paymentID); state != "confirmed" {
		t.Fatalf("payment state = %q, want confirmed", state)
	}
	rows = loadTxRows(t, ctx, database, paymentID)
	var confirmed *txRow
	for i := range rows {
		if rows[i].status == "confirmed" {
			confirmed = &rows[i]
		}
	}
	if confirmed == nil {
		t.Fatalf("no confirmed transaction row: %+v", rows)
	}
	if confirmed.txHash != replacement.txHash {
		t.Fatalf("finalized hash = %s, want the replacement %s", confirmed.txHash, replacement.txHash)
	}
	if confirmed.txHash == originalHash {
		t.Fatal("the payment finalized on the original hash; the replacement mined")
	}
}
