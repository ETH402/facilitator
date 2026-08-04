package merchant

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/ETH402/facilitator/internal/secret"
	"github.com/ETH402/facilitator/internal/walletproof"
	"github.com/jackc/pgx/v5"
)

const adminSessionPrefix = "eth402_admin_"

type AdminSession struct {
	Token     string
	ExpiresAt time.Time
}

type AdminPrincipal struct {
	Merchant            Merchant
	WalletAuthenticated bool
}

type MerchantStats struct {
	ObservedSince         time.Time `json:"observed_since"`
	VerifiedPayments      int64     `json:"verified_payments"`
	PendingSettlements    int64     `json:"pending_settlements"`
	ConfirmedSettlements  int64     `json:"confirmed_settlements"`
	FailedSettlements     int64     `json:"failed_settlements"`
	ConfirmedVolumeAtomic string    `json:"confirmed_volume_atomic"`
	ConfirmedVolumeUSDC   string    `json:"confirmed_volume_usdc"`
}

type PublicMerchant struct {
	Name                 string     `json:"name"`
	Website              *string    `json:"website,omitempty"`
	ConfirmedSettlements int64      `json:"confirmed_settlements"`
	LastConfirmedAt      *time.Time `json:"last_confirmed_at,omitempty"`
}

func (s *Service) newAdminSession() (AdminSession, error) {
	if s.cfg.AdminSessionTTL <= 0 {
		return AdminSession{}, ErrInvalid
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return AdminSession{}, err
	}
	token := adminSessionPrefix + base64.RawURLEncoding.EncodeToString(raw[:])
	now := s.now().UTC()
	return AdminSession{Token: token, ExpiresAt: now.Add(s.cfg.AdminSessionTTL)}, nil
}

func (s *Service) AuthenticateAdmin(ctx context.Context, token string) (AdminPrincipal, error) {
	if !strings.HasPrefix(token, adminSessionPrefix) || len(token) > 128 {
		return AdminPrincipal{}, ErrUnauthorized
	}
	var m Merchant
	var walletVerifiedAt *time.Time
	err := s.pool.QueryRow(ctx, `SELECT m.id,m.name,m.business_email,m.recipient_address,m.status,
		m.website,m.description,m.email_verified_at,m.wallet_verified_at,m.stats_opted_in_at,
		m.public_profile_opted_in_at,m.created_at,
		session.wallet_verified_at
		FROM merchant_admin_sessions session
		JOIN merchants m ON m.id=session.merchant_id
		WHERE session.token_hash=$1 AND session.revoked_at IS NULL
		  AND session.expires_at>$2 AND m.status <> 'rejected'`,
		secret.Hash(token), s.now().UTC()).Scan(
		&m.ID, &m.Name, &m.Email, &m.Recipient, &m.Status, &m.Website, &m.Description,
		&m.EmailVerifiedAt, &m.WalletVerifiedAt, &m.StatsOptedInAt, &m.PublicProfileOptedInAt,
		&m.CreatedAt, &walletVerifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdminPrincipal{}, ErrUnauthorized
	}
	if err != nil {
		return AdminPrincipal{}, err
	}
	walletAuthenticated := walletVerifiedAt != nil && m.WalletVerifiedAt != nil &&
		!walletVerifiedAt.Before(*m.WalletVerifiedAt)
	return AdminPrincipal{Merchant: m, WalletAuthenticated: walletAuthenticated}, nil
}

func (s *Service) VerifyAdminWallet(ctx context.Context, merchantID, sessionToken, challengeID, message, signature, requestID string) error {
	if !validUUID(merchantID) || !strings.HasPrefix(sessionToken, adminSessionPrefix) ||
		!validUUID(challengeID) || len(message) == 0 || len(message) > 4096 || len(signature) != 132 {
		return ErrInvalid
	}
	var address, action, messageHash string
	var expires time.Time
	var consumed *time.Time
	err := s.pool.QueryRow(ctx, `SELECT address,action,message_hash,expires_at,consumed_at
		FROM wallet_verification_challenges WHERE id=$1 AND merchant_id=$2`, challengeID, merchantID).
		Scan(&address, &action, &messageHash, &expires, &consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if action != "authenticate_admin" || consumed != nil || !s.now().Before(expires) ||
		!strings.EqualFold(messageHash, secret.Hash(message)) {
		return ErrInvalid
	}
	if err := walletproof.VerifyMessage(message, signature, merchantID, address, "authenticate-admin", s.now()); err != nil {
		return ErrInvalid
	}
	now := s.now().UTC()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var currentAddress, merchantStatus string
	if err = tx.QueryRow(ctx, `SELECT recipient_address,status FROM merchants WHERE id=$1 FOR UPDATE`, merchantID).
		Scan(&currentAddress, &merchantStatus); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if merchantStatus != "active" || !strings.EqualFold(currentAddress, address) {
		return ErrForbidden
	}
	tag, err := tx.Exec(ctx, `UPDATE wallet_verification_challenges SET consumed_at=$2
		WHERE id=$1 AND consumed_at IS NULL`, challengeID, now)
	if err != nil || tag.RowsAffected() != 1 {
		return ErrConflict
	}
	tag, err = tx.Exec(ctx, `UPDATE merchant_admin_sessions SET wallet_verified_at=$3
		WHERE merchant_id=$1 AND token_hash=$2 AND revoked_at IS NULL AND expires_at>$3`,
		merchantID, secret.Hash(sessionToken), now)
	if err != nil || tag.RowsAffected() != 1 {
		return ErrUnauthorized
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(event_type,merchant_id,actor_type,actor_id,request_id)
		VALUES ('merchant.admin_wallet_authenticated',$1,'merchant',$2,$3)`, merchantID, merchantID, requestID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) MarkAdminSessionWalletVerified(ctx context.Context, merchantID, sessionToken string) error {
	if !validUUID(merchantID) || !strings.HasPrefix(sessionToken, adminSessionPrefix) {
		return ErrInvalid
	}
	now := s.now().UTC()
	tag, err := s.pool.Exec(ctx, `UPDATE merchant_admin_sessions SET wallet_verified_at=$3
		WHERE merchant_id=$1 AND token_hash=$2 AND revoked_at IS NULL AND expires_at>$3`,
		merchantID, secret.Hash(sessionToken), now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrUnauthorized
	}
	return nil
}

func (s *Service) RevokeAdminSession(ctx context.Context, token string) error {
	if !strings.HasPrefix(token, adminSessionPrefix) || len(token) > 128 {
		return ErrUnauthorized
	}
	_, err := s.pool.Exec(ctx, `UPDATE merchant_admin_sessions SET revoked_at=$2
		WHERE token_hash=$1 AND revoked_at IS NULL`, secret.Hash(token), s.now().UTC())
	return err
}

func (s *Service) SetStatsConsent(ctx context.Context, merchantID string, enabled bool, requestID string) (*time.Time, error) {
	if !validUUID(merchantID) {
		return nil, ErrInvalid
	}
	now := s.now().UTC()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	var existing *time.Time
	if err = tx.QueryRow(ctx, `SELECT status,stats_opted_in_at FROM merchants WHERE id=$1 FOR UPDATE`, merchantID).
		Scan(&status, &existing); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	if status != "active" {
		return nil, ErrForbidden
	}
	value := existing
	event := "merchant.stats_opted_out"
	if enabled {
		event = "merchant.stats_opted_in"
		if value == nil {
			value = &now
		}
	} else {
		value = nil
	}
	if _, err = tx.Exec(ctx, `UPDATE merchants SET stats_opted_in_at=$2,updated_at=$3 WHERE id=$1`, merchantID, value, now); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(event_type,merchant_id,actor_type,actor_id,request_id)
		VALUES ($1,$2,'merchant',$3,$4)`, event, merchantID, merchantID, requestID); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return value, nil
}

func (s *Service) SetPublicProfileConsent(ctx context.Context, merchantID string, enabled bool, requestID string) (*time.Time, error) {
	if !validUUID(merchantID) {
		return nil, ErrInvalid
	}
	now := s.now().UTC()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	var existing *time.Time
	if err = tx.QueryRow(ctx, `SELECT status,public_profile_opted_in_at FROM merchants WHERE id=$1 FOR UPDATE`, merchantID).
		Scan(&status, &existing); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	if status != "active" {
		return nil, ErrForbidden
	}
	value := existing
	event := "merchant.public_profile_opted_out"
	if enabled {
		event = "merchant.public_profile_opted_in"
		if value == nil {
			value = &now
		}
	} else {
		value = nil
	}
	if _, err = tx.Exec(ctx, `UPDATE merchants SET public_profile_opted_in_at=$2,updated_at=$3 WHERE id=$1`, merchantID, value, now); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(event_type,merchant_id,actor_type,actor_id,request_id)
		VALUES ($1,$2,'merchant',$3,$4)`, event, merchantID, merchantID, requestID); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	s.invalidatePublicLeaderboard()
	return value, nil
}

// PublicLeaderboard returns only fields a wallet-authenticated merchant opted
// into publishing. Counts begin at the public opt-in timestamp; no amounts,
// addresses, email, payer identity, or pre-consent activity leave this method.
func (s *Service) PublicLeaderboard(ctx context.Context, limit int) ([]PublicMerchant, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrInvalid
	}
	s.publicMu.Lock()
	defer s.publicMu.Unlock()
	if s.now().Before(s.publicExpires) {
		return append([]PublicMerchant(nil), s.publicCached[:min(limit, len(s.publicCached))]...), nil
	}
	rows, err := s.pool.Query(ctx, `SELECT m.name,m.website,count(p.id),max(p.confirmed_at)
		FROM merchants m
		LEFT JOIN payment_records p ON p.merchant_id=m.id AND p.state='confirmed'
			AND p.created_at >= m.public_profile_opted_in_at
		WHERE m.status='active' AND m.public_profile_opted_in_at IS NOT NULL
		GROUP BY m.id,m.name,m.website
		ORDER BY count(p.id) DESC, max(p.confirmed_at) DESC NULLS LAST, lower(m.name),m.id
		LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]PublicMerchant, 0)
	for rows.Next() {
		var item PublicMerchant
		if err = rows.Scan(&item.Name, &item.Website, &item.ConfirmedSettlements, &item.LastConfirmedAt); err != nil {
			return nil, err
		}
		if item.Website != nil {
			parsed, parseErr := url.Parse(*item.Website)
			if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" {
				item.Website = nil
			}
		}
		result = append(result, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	s.publicCached = append(s.publicCached[:0], result...)
	s.publicExpires = s.now().Add(s.cfg.PublicDirectoryTTL)
	return append([]PublicMerchant(nil), result[:min(limit, len(result))]...), nil
}

func (s *Service) invalidatePublicLeaderboard() {
	s.publicMu.Lock()
	s.publicCached = nil
	s.publicExpires = time.Time{}
	s.publicMu.Unlock()
}

func (s *Service) Stats(ctx context.Context, merchantID string) (MerchantStats, error) {
	if !validUUID(merchantID) {
		return MerchantStats{}, ErrInvalid
	}
	var optedIn time.Time
	err := s.pool.QueryRow(ctx, `SELECT stats_opted_in_at FROM merchants
		WHERE id=$1 AND status='active' AND stats_opted_in_at IS NOT NULL`, merchantID).Scan(&optedIn)
	if errors.Is(err, pgx.ErrNoRows) {
		return MerchantStats{}, ErrForbidden
	}
	if err != nil {
		return MerchantStats{}, err
	}
	observedSince := optedIn
	retentionBoundary := s.now().UTC().Add(-s.cfg.PaymentRetention)
	if observedSince.Before(retentionBoundary) {
		observedSince = retentionBoundary
	}
	var result MerchantStats
	result.ObservedSince = observedSince
	err = s.pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE verification_status='verified'),
		count(*) FILTER (WHERE state IN ('broadcasting','broadcast','confirming','replaced','manual_review')),
		count(*) FILTER (WHERE state='confirmed'),
		count(*) FILTER (WHERE state IN ('failed','reverted','expired','verification_failed')),
		coalesce(sum(amount_atomic) FILTER (WHERE state='confirmed'),0)::text
		FROM payment_records WHERE merchant_id=$1 AND created_at >= $2`, merchantID, observedSince).Scan(
		&result.VerifiedPayments, &result.PendingSettlements, &result.ConfirmedSettlements,
		&result.FailedSettlements, &result.ConfirmedVolumeAtomic)
	if err != nil {
		return MerchantStats{}, err
	}
	result.ConfirmedVolumeUSDC = formatUSDC(result.ConfirmedVolumeAtomic)
	return result, nil
}

func formatUSDC(atomic string) string {
	if atomic == "" {
		atomic = "0"
	}
	if len(atomic) <= 6 {
		return "0." + strings.Repeat("0", 6-len(atomic)) + atomic
	}
	return atomic[:len(atomic)-6] + "." + atomic[len(atomic)-6:]
}
