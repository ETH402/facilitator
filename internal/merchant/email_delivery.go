package merchant

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/ETH402/facilitator/internal/email"
	"github.com/jackc/pgx/v5"
)

const (
	emailRetryMinimum  = 5 * time.Second
	emailRetryMaximum  = 5 * time.Minute
	emailDeliveryBatch = 25
)

type emailDelivery struct {
	id, tokenID, tokenHash, merchantID string
	claimToken                         string
	kind, recipient                    string
	ciphertext                         []byte
	expiresAt                          time.Time
	requestID                          *string
	attempts                           int
}

func normalizeEmailOutboxKey(key []byte) [32]byte {
	var normalized [32]byte
	copy(normalized[:], key)
	return normalized
}

func emailTokenAAD(merchantID, tokenHash, kind string) []byte {
	return []byte("eth402-email-outbox-v1\x00" + merchantID + "\x00" + tokenHash + "\x00" + kind)
}

func sealEmailToken(key [32]byte, token, merchantID, tokenHash, kind string) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, []byte(token), emailTokenAAD(merchantID, tokenHash, kind)), nil
}

func openEmailToken(key [32]byte, ciphertext []byte, merchantID, tokenHash, kind string) (string, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(ciphertext) < aead.NonceSize() {
		return "", errors.New("email token ciphertext is truncated")
	}
	plain, err := aead.Open(nil, ciphertext[:aead.NonceSize()], ciphertext[aead.NonceSize():],
		emailTokenAAD(merchantID, tokenHash, kind))
	if err != nil {
		return "", errors.New("email token ciphertext authentication failed")
	}
	return string(plain), nil
}

func (s *Service) enqueueEmail(ctx context.Context, tx pgx.Tx, merchantID, tokenHash, rawToken, kind, requestID string, now time.Time) (string, error) {
	var tokenID string
	if err := tx.QueryRow(ctx, `INSERT INTO email_verification_tokens
		(merchant_id,token_hash,expires_at,created_at)
		VALUES ($1,$2,$3,$4) RETURNING id`,
		merchantID, tokenHash, now.Add(s.cfg.EmailTTL), now).Scan(&tokenID); err != nil {
		return "", err
	}
	ciphertext, err := sealEmailToken(s.emailOutboxKey, rawToken, merchantID, tokenHash, kind)
	if err != nil {
		return "", err
	}
	var deliveryID string
	if err := tx.QueryRow(ctx, `INSERT INTO email_delivery_outbox
		(merchant_id,token_id,message_kind,token_ciphertext,request_id,next_attempt_at,created_at,updated_at)
		VALUES ($1,$2,$3,$4,nullif($5,''),$6,$6,$6) RETURNING id`,
		merchantID, tokenID, kind, ciphertext, requestID, now).Scan(&deliveryID); err != nil {
		return "", err
	}
	return deliveryID, nil
}

func (s *Service) wakeEmailDelivery() {
	select {
	case s.emailWake <- struct{}{}:
	default:
	}
}

// DeliverPendingEmail performs one bounded retry batch. It is exported for the
// process worker and deterministic integration tests, not as a public API.
func (s *Service) DeliverPendingEmail(ctx context.Context) (int, error) {
	processed := 0
	for range emailDeliveryBatch {
		found, err := s.deliverNext(ctx, "")
		if err != nil {
			return processed, err
		}
		if !found {
			return processed, nil
		}
		processed++
	}
	return processed, nil
}

// RunEmailDelivery retries the durable outbox immediately at startup and then
// on the supplied cadence. A failed tick never stops later retries.
func (s *Service) RunEmailDelivery(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		processed, err := s.DeliverPendingEmail(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			s.logger.WarnContext(ctx, "email outbox retry failed", "reason", "database_or_delivery_state")
		} else if err == nil {
			if observeErr := s.observeEmailOutbox(ctx); observeErr != nil && !errors.Is(observeErr, context.Canceled) {
				s.logger.WarnContext(ctx, "email outbox observation failed", "reason", "database_state")
			}
		}
		if processed == emailDeliveryBatch {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.emailWake:
		}
	}
}

func (s *Service) deliverNext(ctx context.Context, deliveryID string) (bool, error) {
	now := s.now().UTC()
	// Expired links are never sent. Erasing ciphertext here also makes a stuck
	// outbox row harmless even before the ordinary retention pass deletes it.
	if _, err := s.pool.Exec(ctx, `WITH expired AS (
		SELECT outbox.id FROM email_delivery_outbox outbox
		JOIN email_verification_tokens token ON token.id=outbox.token_id
		WHERE outbox.delivered_at IS NULL AND outbox.abandoned_at IS NULL AND token.expires_at<=$1
		  AND (outbox.claimed_until IS NULL OR outbox.claimed_until<=$1)
		ORDER BY token.expires_at FOR UPDATE OF outbox SKIP LOCKED LIMIT 100
	)
		UPDATE email_delivery_outbox outbox
		SET abandoned_at=$1,token_ciphertext=NULL,claimed_until=NULL,claim_token=NULL,updated_at=$1
		FROM expired WHERE outbox.id=expired.id`, now); err != nil {
		return false, err
	}

	var item emailDelivery
	err := s.pool.QueryRow(ctx, `WITH candidate AS (
		SELECT outbox.id
		FROM email_delivery_outbox outbox
		JOIN email_verification_tokens token ON token.id=outbox.token_id
		WHERE outbox.delivered_at IS NULL AND outbox.abandoned_at IS NULL
		  AND outbox.next_attempt_at<=$1
		  AND (outbox.claimed_until IS NULL OR outbox.claimed_until<=$1)
		  AND token.expires_at>$1
		  AND ($2::uuid IS NULL OR outbox.id=$2)
		ORDER BY outbox.created_at
		FOR UPDATE OF outbox SKIP LOCKED
		LIMIT 1
	)
	UPDATE email_delivery_outbox outbox
	SET claimed_until=$3,claim_token=gen_random_uuid(),attempts=outbox.attempts+1,updated_at=$1
	FROM candidate, email_verification_tokens token, merchants merchant
	WHERE outbox.id=candidate.id AND token.id=outbox.token_id AND merchant.id=outbox.merchant_id
	RETURNING outbox.id,outbox.token_id,token.token_hash,outbox.merchant_id,outbox.claim_token,outbox.message_kind,
		outbox.token_ciphertext,merchant.business_email,token.expires_at,outbox.request_id,outbox.attempts`,
		now, nullableUUID(deliveryID), now.Add(s.emailClaim)).Scan(
		&item.id, &item.tokenID, &item.tokenHash, &item.merchantID, &item.claimToken, &item.kind, &item.ciphertext,
		&item.recipient, &item.expiresAt, &item.requestID, &item.attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	rawToken, err := openEmailToken(s.emailOutboxKey, item.ciphertext,
		item.merchantID, item.tokenHash, item.kind)
	if err != nil {
		return true, s.abandonCorruptEmail(ctx, item)
	}
	link := strings.TrimRight(s.cfg.BaseURL, "/") + "/verify-email?token=" + url.QueryEscape(rawToken)
	message := email.Message{To: item.recipient, Subject: "Verify your ETH402 email", TextBody: "Verify your email: " + link}
	if item.kind == "admin_login" {
		message.Subject = "Sign in to ETH402"
		message.TextBody = "Sign in to your ETH402 merchant panel: " + link
	}
	err = s.mail.Send(ctx, message)
	if err != nil {
		if s.emailObserver != nil {
			s.emailObserver.ObserveEmailDeliveryFailure()
		}
		finishedAt := s.now().UTC()
		delay := retryDelay(item.attempts)
		tag, updateErr := s.pool.Exec(ctx, `UPDATE email_delivery_outbox
			SET claimed_until=NULL,claim_token=NULL,next_attempt_at=$2,updated_at=$1
			WHERE id=$3 AND claim_token=$4 AND delivered_at IS NULL AND abandoned_at IS NULL`,
			finishedAt, finishedAt.Add(delay), item.id, item.claimToken)
		if updateErr != nil {
			return true, fmt.Errorf("release failed email delivery claim: %w", updateErr)
		}
		if tag.RowsAffected() == 1 {
			s.logDeliveryFailure(ctx, item.id, item.requestID, err)
		}
		return true, nil
	}

	deliveredAt := s.now().UTC()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return true, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE email_delivery_outbox
		SET delivered_at=$2,token_ciphertext=NULL,claimed_until=NULL,claim_token=NULL,updated_at=$2
		WHERE id=$1 AND claim_token=$3 AND delivered_at IS NULL AND abandoned_at IS NULL`,
		item.id, deliveredAt, item.claimToken)
	if err != nil {
		return true, err
	}
	if tag.RowsAffected() != 1 {
		// A newer worker reclaimed this delivery after our lease expired. SMTP may
		// already have accepted our copy, but only the current owner may mutate
		// durable state; it will safely retry the same one-time token if needed.
		return true, nil
	}
	if _, err = tx.Exec(ctx, `UPDATE email_verification_tokens
		SET sent_at=$2 WHERE id=$1 AND sent_at IS NULL`, item.tokenID, deliveredAt); err != nil {
		return true, err
	}
	if err = tx.Commit(ctx); err != nil {
		return true, err
	}
	return true, nil
}

func (s *Service) abandonCorruptEmail(ctx context.Context, item emailDelivery) error {
	now := s.now().UTC()
	tag, err := s.pool.Exec(ctx, `UPDATE email_delivery_outbox
		SET abandoned_at=$2,token_ciphertext=NULL,claimed_until=NULL,claim_token=NULL,updated_at=$2
		WHERE id=$1 AND claim_token=$3 AND delivered_at IS NULL AND abandoned_at IS NULL`,
		item.id, now, item.claimToken)
	if err != nil {
		return fmt.Errorf("abandon unreadable email delivery: %w", err)
	}
	if tag.RowsAffected() == 1 {
		if s.emailObserver != nil {
			s.emailObserver.ObserveEmailDeliveryFailure()
		}
		attributes := []any{
			"delivery_id", item.id,
			"reason", "payload_authentication_failed",
		}
		if item.requestID != nil && *item.requestID != "" {
			attributes = append(attributes, "request_id", *item.requestID)
		}
		s.logger.ErrorContext(ctx, "email delivery payload abandoned", attributes...)
	}
	return nil
}

func (s *Service) observeEmailOutbox(ctx context.Context) error {
	if s.emailObserver == nil {
		return nil
	}
	now := s.now().UTC()
	var pending int64
	var oldest *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT count(*),min(created_at)
		FROM email_delivery_outbox
		WHERE delivered_at IS NULL AND abandoned_at IS NULL`).Scan(&pending, &oldest); err != nil {
		return err
	}
	age := time.Duration(0)
	if oldest != nil && oldest.Before(now) {
		age = now.Sub(*oldest)
	}
	s.emailObserver.ObserveEmailOutbox(pending, age, now)
	return nil
}

func (s *Service) logDeliveryFailure(ctx context.Context, deliveryID string, requestID *string, _ error) {
	attributes := []any{"delivery_id", deliveryID, "reason", "delivery_failed"}
	if requestID != nil && *requestID != "" {
		attributes = append(attributes, "request_id", *requestID)
	}
	s.logger.WarnContext(ctx, "email delivery failed; queued for retry", attributes...)
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		return emailRetryMinimum
	}
	delay := emailRetryMinimum
	for i := 1; i < attempt && delay < emailRetryMaximum; i++ {
		delay *= 2
	}
	if delay > emailRetryMaximum {
		return emailRetryMaximum
	}
	return delay
}

func nullableUUID(value string) any {
	if value == "" {
		return nil
	}
	return value
}
