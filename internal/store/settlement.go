package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ETH402/facilitator/internal/settlement"
	"github.com/ETH402/facilitator/internal/walletproof"
	"github.com/jackc/pgx/v5"
)

// activeTransactionStatuses mirrors the ethereum_transactions_active_payment_unique
// partial index. The index is the real guarantee of one in-flight transaction per
// payment; this list only lets the duplicate be reported instead of raising.
var activeTransactionStatuses = []string{"intent", "broadcasting", "broadcast", "confirming", "ambiguous"}

// CreateSettlementIntent durably reserves an Ethereum nonce and records the
// intent to settle a verified payment. It signs nothing and broadcasts nothing.
//
// Everything happens in one transaction: the payment row is locked, admission is
// checked, the nonce is allocated, the payment moves to broadcasting, and the
// ethereum_transactions row lands with status 'intent'. Committing before any
// signing is what makes ADR-0004's "never blindly retry" enforceable — after a
// crash there is always a durable row saying which nonce was spoken for.
//
// Rejections are committed as settlement_attempts rows and returned as sentinel
// errors, so a refusal is auditable rather than merely logged.
func (s *Store) CreateSettlementIntent(ctx context.Context, request settlement.IntentRequest) (settlement.Intent, error) {
	if err := request.Validate(); err != nil {
		return settlement.Intent{}, err
	}
	signerAddress, err := walletproof.NormalizeAddress(request.SignerAddress)
	if err != nil {
		return settlement.Intent{}, fmt.Errorf("signer address: %w", err)
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return settlement.Intent{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// FOR UPDATE serialises concurrent settlement of the same payment. Nonce
	// allocation is deliberately deferred until after every admission check so a
	// rejected request cannot consume a nonce and gap the sequence.
	var paymentID, state string
	var merchantID, storedSignature *string
	var validBefore *time.Time
	err = tx.QueryRow(ctx, `
SELECT id, state, merchant_id, valid_before, payer_signature
FROM payment_records
WHERE payment_identity = $1
FOR UPDATE`, request.PaymentIdentity).Scan(&paymentID, &state, &merchantID, &validBefore, &storedSignature)
	if errors.Is(err, pgx.ErrNoRows) {
		return settlement.Intent{}, rejectSettlement(ctx, tx, nil, request.PaymentIdentity,
			settlement.ReasonPaymentNotFound, settlement.ErrPaymentNotFound)
	}
	if err != nil {
		return settlement.Intent{}, err
	}

	// A duplicate /settle must observe the terminal outcome, not a fresh
	// attempt: confirmed payments return the recorded hash as a success,
	// reverted payments return the same hash as transaction_reverted. Callers
	// retrying a lost response therefore always see the true outcome rather
	// than a misleading payment_not_verified rejection — or a success for a
	// transaction that reverted.
	if state == string(settlement.StateConfirmed) || state == string(settlement.StateReverted) {
		var terminalID, terminalNonce, terminalSigner, terminalHash string
		err = tx.QueryRow(ctx, `
SELECT id, transaction_nonce::text, signer_address, tx_hash
FROM ethereum_transactions
WHERE payment_id = $1 AND status = $2 AND tx_hash IS NOT NULL
ORDER BY updated_at DESC
LIMIT 1`, paymentID, state).Scan(&terminalID, &terminalNonce, &terminalSigner, &terminalHash)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return settlement.Intent{}, fmt.Errorf("load terminal settlement transaction: %w", err)
		}
		if err == nil {
			nonce, parseErr := parseNonce(terminalNonce)
			if parseErr != nil {
				return settlement.Intent{}, parseErr
			}
			if _, err := tx.Exec(ctx, `
INSERT INTO settlement_attempts(payment_id,payment_identity,result)
VALUES ($1,$2,'duplicate')`, paymentID, request.PaymentIdentity); err != nil {
				return settlement.Intent{}, err
			}
			if err := tx.Commit(ctx); err != nil {
				return settlement.Intent{}, err
			}
			return settlement.Intent{
				PaymentID: paymentID, PaymentIdentity: request.PaymentIdentity,
				TransactionID: terminalID, SignerAddress: terminalSigner,
				Nonce: nonce, TxHash: terminalHash, Duplicate: true,
				Reverted:  state == string(settlement.StateReverted),
				Confirmed: state == string(settlement.StateConfirmed),
			}, nil
		}
	}

	// An existing active transaction means this payment is already being settled.
	// Report the existing intent rather than allocating a second nonce for it.
	var existingID, existingNonce, existingSigner, existingHash string
	err = tx.QueryRow(ctx, `
SELECT id, transaction_nonce::text, signer_address, coalesce(tx_hash, '')
FROM ethereum_transactions
WHERE payment_id = $1 AND status = ANY($2)`, paymentID, activeTransactionStatuses).
		Scan(&existingID, &existingNonce, &existingSigner, &existingHash)
	if err == nil {
		nonce, parseErr := parseNonce(existingNonce)
		if parseErr != nil {
			return settlement.Intent{}, parseErr
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO settlement_attempts(payment_id,payment_identity,result)
VALUES ($1,$2,'duplicate')`, paymentID, request.PaymentIdentity); err != nil {
			return settlement.Intent{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return settlement.Intent{}, err
		}
		return settlement.Intent{
			PaymentID: paymentID, PaymentIdentity: request.PaymentIdentity,
			TransactionID: existingID, SignerAddress: existingSigner,
			Nonce: nonce, TxHash: existingHash, Duplicate: true,
		}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return settlement.Intent{}, err
	}

	if err := settlement.ValidateTransition(settlement.State(state), settlement.StateBroadcasting); err != nil {
		return settlement.Intent{}, rejectSettlement(ctx, tx, &paymentID, request.PaymentIdentity,
			settlement.ReasonPaymentNotVerified, fmt.Errorf("%w: %s", settlement.ErrPaymentNotVerified, state))
	}
	if merchantID == nil {
		return settlement.Intent{}, rejectSettlement(ctx, tx, &paymentID, request.PaymentIdentity,
			settlement.ReasonRecipientNotMerchant, settlement.ErrRecipientNotMerchant)
	}
	// Lock the merchant row before evaluating merchant-scoped policy. The
	// payment lock above serialises duplicate settlement of one authorization;
	// it cannot serialise different payments attributed to the same merchant.
	// Without this second lock, concurrent requests can all count the same
	// pre-limit snapshot and collectively exceed the gas quota.
	//
	// Requiring status='active' here also makes suspension effective at
	// settlement time. Verification records the merchant attribution, but that
	// durable association must not let a payment verified before suspension
	// bypass the current admission policy.
	var activeMerchantID string
	err = tx.QueryRow(ctx, `
SELECT id FROM merchants
WHERE id = $1 AND status = 'active'
FOR UPDATE`, *merchantID).Scan(&activeMerchantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return settlement.Intent{}, rejectSettlement(ctx, tx, &paymentID, request.PaymentIdentity,
			settlement.ReasonRecipientNotMerchant, settlement.ErrRecipientNotMerchant)
	}
	if err != nil {
		return settlement.Intent{}, fmt.Errorf("lock active merchant: %w", err)
	}
	if validBefore == nil || !validBefore.After(request.Now.Add(request.ExpiryMargin)) {
		return settlement.Intent{}, rejectSettlement(ctx, tx, &paymentID, request.PaymentIdentity,
			settlement.ReasonAuthorizationExpiring, settlement.ErrAuthorizationExpiring)
	}
	// The quota is counted while the merchant row lock is held, so every
	// settlement for this merchant observes the preceding request's committed
	// result before deciding. settlement_requested_at is stamped only here, which
	// makes this exactly the number of settlement intents committed on the
	// merchant's behalf — including intents retired before broadcast, whose gas
	// was still risked; replacements and gap fillers reuse existing rows and
	// correctly do not count against it. The window is measured on the database
	// clock because the stamps are: the application clock can be skewed.
	var settled int
	if err := tx.QueryRow(ctx, `
SELECT count(*) FROM payment_records
WHERE merchant_id = $1 AND settlement_requested_at > now() - make_interval(secs => $2)`,
		*merchantID, request.QuotaWindow.Seconds()).Scan(&settled); err != nil {
		return settlement.Intent{}, fmt.Errorf("count merchant settlements: %w", err)
	}
	if settled >= request.Quota {
		return settlement.Intent{}, rejectSettlement(ctx, tx, &paymentID, request.PaymentIdentity,
			settlement.ReasonMerchantQuotaExceeded, settlement.ErrMerchantQuotaExceeded)
	}
	// The facilitator-wide ceiling. The merchant row lock above serialises one
	// merchant's requests but says nothing about two merchants settling at once, so
	// without a lock covering every merchant, concurrent requests would each count
	// the same pre-limit snapshot and collectively exceed the total — exactly the
	// bug the merchant lock was added to fix, one level up.
	//
	// This serialises settlement admission across all merchants, which costs
	// nothing real: every settlement must allocate a nonce from the same
	// signer_accounts row a few lines below, so admission was already one at a
	// time. Lock order is payment → merchant row → global → signer row, the same
	// in every transaction, so the added lock introduces no deadlock cycle.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, globalSettlementLockKey); err != nil {
		return settlement.Intent{}, fmt.Errorf("take global settlement lock: %w", err)
	}
	var settledOverall int
	if err := tx.QueryRow(ctx, `
SELECT count(*) FROM payment_records
WHERE settlement_requested_at > now() - make_interval(secs => $1)`,
		request.QuotaWindow.Seconds()).Scan(&settledOverall); err != nil {
		return settlement.Intent{}, fmt.Errorf("count facilitator settlements: %w", err)
	}
	if settledOverall >= request.GlobalQuota {
		return settlement.Intent{}, rejectSettlement(ctx, tx, &paymentID, request.PaymentIdentity,
			settlement.ReasonGlobalQuotaExceeded, settlement.ErrGlobalQuotaExceeded)
	}
	// The payment identity hash binds the signature, so a stored value can only
	// ever equal the request's. A mismatch means the durable record was written
	// by something outside this flow; fail loudly rather than sign over it.
	if storedSignature != nil && *storedSignature != request.PayerSignature {
		return settlement.Intent{}, fmt.Errorf("payment %s: stored payer signature conflicts with the settlement request", request.PaymentIdentity)
	}

	nonce, err := AllocateNonce(ctx, tx, signerAddress)
	if err != nil {
		return settlement.Intent{}, err
	}

	tag, err := tx.Exec(ctx, `
UPDATE payment_records
SET state = 'broadcasting', settlement_requested_at = now(),
    payer_signature = $3, updated_at = now()
WHERE id = $1 AND state = $2`, paymentID, state, request.PayerSignature)
	if err != nil {
		return settlement.Intent{}, err
	}
	if tag.RowsAffected() != 1 {
		// The row lock should make this unreachable; treat it as a lost race
		// rather than proceeding against an unknown state.
		return settlement.Intent{}, fmt.Errorf("payment %s changed state during settlement", request.PaymentIdentity)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO payment_transitions(payment_id,from_state,to_state,actor_type,metadata)
VALUES ($1,$2,'broadcasting','http','{}'::jsonb)`, paymentID, state); err != nil {
		return settlement.Intent{}, err
	}

	var transactionID string
	if err := tx.QueryRow(ctx, `
INSERT INTO ethereum_transactions(payment_id,signer_address,transaction_nonce,status)
VALUES ($1,$2,$3,'intent')
RETURNING id`, paymentID, signerAddress, nonce).Scan(&transactionID); err != nil {
		return settlement.Intent{}, err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO settlement_attempts(payment_id,payment_identity,result)
VALUES ($1,$2,'accepted')`, paymentID, request.PaymentIdentity); err != nil {
		return settlement.Intent{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return settlement.Intent{}, err
	}
	return settlement.Intent{
		PaymentID: paymentID, PaymentIdentity: request.PaymentIdentity,
		TransactionID: transactionID, SignerAddress: signerAddress, Nonce: nonce,
	}, nil
}

// rejectSettlement records the refused attempt inside the caller's transaction
// and commits it, then returns the caller's sentinel error.
//
// The write must share that transaction. settlement_attempts references
// payment_records, so the foreign-key check needs FOR KEY SHARE on the parent
// row — which the caller's own FOR UPDATE holds. Recording the rejection on a
// second connection therefore blocks on a lock the blocked caller is holding,
// and PostgreSQL cannot report it as a deadlock because only one side is waiting
// on a lock while the other waits on the client.
//
// Nothing has been written before this point, so committing publishes only the
// audit row.
func rejectSettlement(ctx context.Context, tx pgx.Tx, paymentID *string, identity, reason string, cause error) error {
	var id any
	if paymentID != nil {
		id = *paymentID
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO settlement_attempts(payment_id,payment_identity,result,reason_code)
VALUES ($1,$2,'rejected',$3)`, id, nullString(identity), reason); err != nil {
		return errors.Join(cause, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}
