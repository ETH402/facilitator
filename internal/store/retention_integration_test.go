//go:build integration

package store

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestRetentionRedactsWithoutBreakingStatsOrIdempotency(t *testing.T) {
	store := settlementTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	old := now.Add(-31 * 24 * time.Hour)

	var merchantID string
	if err := store.Pool.QueryRow(ctx, `INSERT INTO merchants
		(name,business_email,email_domain,recipient_address,terms_version,terms_accepted_at,
		 status,email_verified_at,wallet_verified_at)
		VALUES ('Retention','retention@example.com','example.com',
		        '0x1111111111111111111111111111111111111111','v1',$1,'active',$1,$1)
		RETURNING id`, old).Scan(&merchantID); err != nil {
		t.Fatal(err)
	}

	confirmedIdentity := "pay_" + repeat("a", 64)
	var confirmedID string
	if err := store.Pool.QueryRow(ctx, `INSERT INTO payment_records
		(payment_identity,merchant_id,x402_version,scheme,network,asset,payer_address,
		 recipient_address,amount_atomic,authorization_nonce,valid_after,valid_before,
		 payload_hash,verification_status,state,settlement_requested_at,confirmed_at,
		 payer_signature,created_at,updated_at)
		VALUES ($1,$2,2,'exact','eip155:1',
		        '0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48',
		        '0x2222222222222222222222222222222222222222',
		        '0x1111111111111111111111111111111111111111',
		        1000000,$3,$4,$5,$6,'verified','confirmed',$4,$4,$7,$4,$4)
		RETURNING id`, confirmedIdentity, merchantID, nextFixtureNonce(),
		old, old.Add(time.Hour), repeat("b", 64),
		"0x"+repeat("1", 64)+repeat("2", 64)+"1b").Scan(&confirmedID); err != nil {
		t.Fatal(err)
	}
	txHash := "0x" + repeat("c", 64)
	if _, err := store.Pool.Exec(ctx, `INSERT INTO ethereum_transactions
		(payment_id,tx_hash,signer_address,transaction_nonce,raw_transaction_hash,
		 raw_transaction,status,confirmed_at,created_at,updated_at)
		VALUES ($1,$2,$3,0,$4,$5,'confirmed',$6,$6,$6)`,
		confirmedID, txHash, intentSigner, repeat("d", 64), []byte("sensitive signed transaction"), old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool.Exec(ctx, `INSERT INTO verification_attempts
		(payment_id,payment_identity,result,created_at)
		VALUES ($1,$2,'verified',$3)`, confirmedID, confirmedIdentity, old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool.Exec(ctx, `INSERT INTO settlement_attempts
		(payment_id,payment_identity,result,created_at)
		VALUES ($1,$2,'accepted',$3)`, confirmedID, confirmedIdentity, old); err != nil {
		t.Fatal(err)
	}

	droppedIdentity := "pay_" + repeat("e", 64)
	var droppedID string
	if err := store.Pool.QueryRow(ctx, `INSERT INTO payment_records
		(payment_identity,merchant_id,x402_version,scheme,network,asset,payer_address,
		 recipient_address,amount_atomic,authorization_nonce,valid_after,valid_before,
		 payload_hash,verification_status,state,payer_signature,created_at,updated_at)
		VALUES ($1,$2,2,'exact','eip155:1',
		        '0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48',
		        '0x3333333333333333333333333333333333333333',
		        '0x1111111111111111111111111111111111111111',
		        2,$3,$4,$5,$6,'verified','expired',$7,$4,$4)
		RETURNING id`, droppedIdentity, merchantID, nextFixtureNonce(),
		old, old.Add(time.Hour), repeat("f", 64),
		"0x"+repeat("3", 64)+repeat("4", 64)+"1b").Scan(&droppedID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool.Exec(ctx, `INSERT INTO ethereum_transactions
		(payment_id,signer_address,transaction_nonce,status,created_at,updated_at)
		VALUES ($1,$2,1,'dropped',$3,$3)`, droppedID, intentSigner, old); err != nil {
		t.Fatal(err)
	}

	verifiedIdentity := "pay_" + repeat("9", 64)
	var verifiedID string
	if err := store.Pool.QueryRow(ctx, `INSERT INTO payment_records
		(payment_identity,merchant_id,x402_version,scheme,network,asset,payer_address,
		 recipient_address,amount_atomic,authorization_nonce,valid_after,valid_before,
		 payload_hash,verification_status,state,created_at,updated_at)
		VALUES ($1,$2,2,'exact','eip155:1',
		        '0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48',
		        '0x4444444444444444444444444444444444444444',
		        '0x1111111111111111111111111111111111111111',
		        3,$3,$4,$5,$6,'verified','verified',$4,$4)
		RETURNING id`, verifiedIdentity, merchantID, nextFixtureNonce(),
		old, old.Add(time.Hour), repeat("8", 64)).Scan(&verifiedID); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Pool.Exec(ctx, `INSERT INTO email_verification_tokens
		(merchant_id,token_hash,expires_at,sent_at,created_at)
		VALUES ($1,$2,$3,$4,$4)`,
		merchantID, repeat("5", 64), old.Add(time.Hour), old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool.Exec(ctx, `INSERT INTO wallet_verification_challenges
		(merchant_id,address,nonce,message_hash,action,issued_at,expires_at,created_at)
		VALUES ($1,'0x1111111111111111111111111111111111111111',$2,$3,
		        'verify_recipient',$4,$5,$4)`,
		merchantID, repeat("6", 64), repeat("7", 64), old, old.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool.Exec(ctx, `INSERT INTO api_keys
		(merchant_id,name,key_prefix,key_hash,created_at,revoked_at)
		VALUES ($1,'old','old-prefix',$2,$3,$3)`,
		merchantID, repeat("0", 64), old); err != nil {
		t.Fatal(err)
	}

	before, err := store.AggregateStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.ApplyRetention(ctx, RetentionRequest{
		Now: now, PaymentAfter: 30 * 24 * time.Hour,
		EphemeralAfter: 24 * time.Hour, RevokedKeyAfter: 30 * 24 * time.Hour,
		BatchSize: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExpiredPayments != 1 || result.RedactedPayments != 1 ||
		result.EmailTokens != 1 || result.WalletChallenges != 1 || result.RevokedAPIKeys != 1 {
		t.Fatalf("retention result = %+v", result)
	}
	after, err := store.AggregateStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("stats changed across redaction:\nbefore: %+v\nafter:  %+v", before, after)
	}

	var identity, amount, state string
	var merchant, payer, recipient, nonce, payload, signature *string
	var validAfter, validBefore, redactedAt *time.Time
	if err := store.Pool.QueryRow(ctx, `SELECT payment_identity,amount_atomic::text,state,
		merchant_id::text,payer_address,recipient_address,authorization_nonce,payload_hash,
		payer_signature,valid_after,valid_before,redacted_at
		FROM payment_records WHERE id=$1`, confirmedID).Scan(
		&identity, &amount, &state, &merchant, &payer, &recipient, &nonce, &payload,
		&signature, &validAfter, &validBefore, &redactedAt); err != nil {
		t.Fatal(err)
	}
	if identity != confirmedIdentity || amount != "1000000" || state != "confirmed" ||
		merchant != nil || payer != nil || recipient != nil || nonce != nil ||
		payload != nil || signature != nil || validAfter != nil || validBefore != nil ||
		redactedAt == nil {
		t.Fatalf("unexpected payment tombstone")
	}
	var raw []byte
	if err := store.Pool.QueryRow(ctx,
		`SELECT raw_transaction FROM ethereum_transactions WHERE payment_id=$1`,
		confirmedID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != nil {
		t.Fatal("raw transaction survived retention")
	}

	intent, err := store.CreateSettlementIntent(ctx, intentRequest(confirmedIdentity))
	if err != nil {
		t.Fatalf("duplicate settlement after retention: %v", err)
	}
	if !intent.Duplicate || intent.TxHash != txHash {
		t.Fatalf("duplicate intent = %+v", intent)
	}
	var droppedRedacted *time.Time
	if err := store.Pool.QueryRow(ctx,
		`SELECT redacted_at FROM payment_records WHERE id=$1`, droppedID).Scan(&droppedRedacted); err != nil {
		t.Fatal(err)
	}
	if droppedRedacted != nil {
		t.Fatal("nonce-gap authorization was redacted")
	}
	var verifiedState string
	if err := store.Pool.QueryRow(ctx,
		`SELECT state FROM payment_records WHERE id=$1`, verifiedID).Scan(&verifiedState); err != nil {
		t.Fatal(err)
	}
	if verifiedState != "expired" {
		t.Fatalf("stale verified payment state = %q", verifiedState)
	}
}
