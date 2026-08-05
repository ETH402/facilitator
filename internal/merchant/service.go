package merchant

import (
	"context"
	"crypto/hmac"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/mail"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ETH402/facilitator/internal/auth"
	"github.com/ETH402/facilitator/internal/email"
	"github.com/ETH402/facilitator/internal/secret"
	"github.com/ETH402/facilitator/internal/walletproof"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInvalid      = errors.New("invalid merchant request")
	ErrUnauthorized = errors.New("invalid API key")
	ErrForbidden    = errors.New("merchant access forbidden")
	ErrNotFound     = errors.New("merchant resource not found")
	ErrConflict     = errors.New("merchant state conflict")
	ErrThrottled    = errors.New("request is throttled")
)

type Config struct {
	BaseURL, TermsVersion                          string
	EmailTTL, Resend, WalletTTL, RecipientCooldown time.Duration
	AdminSessionTTL, PaymentRetention              time.Duration
	PublicDirectoryTTL                             time.Duration
	EmailDeliveryLease                             time.Duration
	Pepper                                         []byte
	EmailOutboxKey                                 []byte
	BlockDisposable, RestrictFree                  bool
	Allowlist, Denylist                            []string
	Logger                                         *slog.Logger
	EmailObserver                                  EmailDeliveryObserver
	RecipientObserver                              RecipientChangeObserver
}

// EmailDeliveryObserver keeps operational telemetry outside the merchant
// package. Implementations receive only aggregate counts/times and never
// recipient, merchant, request, token, or delivery identifiers.
type EmailDeliveryObserver interface {
	ObserveEmailOutbox(pending int64, oldestPendingAge time.Duration, at time.Time)
	ObserveEmailDeliveryFailure()
}

// RecipientChangeObserver exposes only low-cardinality security event classes.
// It deliberately receives no merchant, wallet, session, or request identifiers.
type RecipientChangeObserver interface {
	ObserveRecipientChange(pending bool)
}

type Service struct {
	pool              *pgxpool.Pool
	mail              email.Sender
	cfg               Config
	now               func() time.Time
	logger            *slog.Logger
	emailOutboxKey    [32]byte
	emailClaim        time.Duration
	emailWake         chan struct{}
	emailObserver     EmailDeliveryObserver
	recipientObserver RecipientChangeObserver
	publicMu          sync.Mutex
	publicCached      []PublicMerchant
	publicExpires     time.Time
}

type Registration struct {
	Name, Email, Recipient, Website, Description string
	AcceptTerms                                  bool
}

type Challenge struct {
	ID        string    `json:"id"`
	Message   string    `json:"message"`
	Address   string    `json:"address"`
	Action    string    `json:"action"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Merchant struct {
	ID                     string     `json:"id"`
	Name                   string     `json:"name"`
	Email                  string     `json:"business_email"`
	Recipient              string     `json:"recipient_address"`
	Status                 string     `json:"status"`
	Website                *string    `json:"website,omitempty"`
	Description            *string    `json:"description,omitempty"`
	EmailVerifiedAt        *time.Time `json:"email_verified_at,omitempty"`
	WalletVerifiedAt       *time.Time `json:"wallet_verified_at,omitempty"`
	StatsOptedInAt         *time.Time `json:"stats_opted_in_at,omitempty"`
	PublicProfileOptedInAt *time.Time `json:"public_profile_opted_in_at,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
}

type APIKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

func New(pool *pgxpool.Pool, sender email.Sender, cfg Config) *Service {
	if cfg.PublicDirectoryTTL <= 0 {
		cfg.PublicDirectoryTTL = 10 * time.Second
	}
	if cfg.EmailDeliveryLease <= 0 {
		cfg.EmailDeliveryLease = time.Minute
	}
	emailOutboxKey := normalizeEmailOutboxKey(cfg.EmailOutboxKey)
	cfg.EmailOutboxKey = nil
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Service{
		pool: pool, mail: sender, cfg: cfg, now: time.Now, logger: logger,
		emailOutboxKey:    emailOutboxKey,
		emailClaim:        cfg.EmailDeliveryLease,
		emailWake:         make(chan struct{}, 1),
		emailObserver:     cfg.EmailObserver,
		recipientObserver: cfg.RecipientObserver,
	}
}

func (s *Service) Register(ctx context.Context, in Registration, requestID string) error {
	in.Name, in.Email, in.Recipient = strings.TrimSpace(in.Name), strings.ToLower(strings.TrimSpace(in.Email)), strings.TrimSpace(in.Recipient)
	if len(in.Name) < 1 || len(in.Name) > 200 || len(in.Email) > 320 ||
		len(in.Website) > 2048 || len(in.Description) > 4000 || !in.AcceptTerms {
		return ErrInvalid
	}
	parsed, err := mail.ParseAddress(in.Email)
	if err != nil || parsed.Address != in.Email || strings.Count(in.Email, "@") != 1 {
		return ErrInvalid
	}
	domain := strings.SplitN(in.Email, "@", 2)[1]
	if err := s.validateDomain(domain); err != nil {
		return err
	}
	recipient, err := walletproof.NormalizeAddress(in.Recipient)
	if err != nil {
		return ErrInvalid
	}
	if in.Website != "" {
		u, err := url.ParseRequestURI(in.Website)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return ErrInvalid
		}
	}
	raw, hash, err := email.NewVerificationToken()
	if err != nil {
		return err
	}
	now := s.now().UTC()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO merchants
		(name,business_email,email_domain,website,description,recipient_address,terms_version,terms_accepted_at)
		VALUES ($1,$2,$3,nullif($4,''),nullif($5,''),$6,$7,$8)
		ON CONFLICT DO NOTHING RETURNING id`, in.Name, in.Email, domain, in.Website, in.Description, recipient, s.cfg.TermsVersion, now).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT id FROM merchants WHERE business_email=$1 AND status <> 'rejected' FOR UPDATE`, in.Email).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			// The conflicting row was rejected between the insert and this
			// read. Stay silent so the response cannot distinguish states.
			return nil
		}
		if err != nil {
			return err
		}
		var suppressed bool
		err = tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM email_verification_tokens token
			LEFT JOIN email_delivery_outbox outbox ON outbox.token_id=token.id
			WHERE token.merchant_id=$1 AND (
				(token.sent_at IS NOT NULL AND token.sent_at>$2) OR
				(token.sent_at IS NULL AND token.expires_at>$3 AND outbox.delivered_at IS NULL
				 AND outbox.abandoned_at IS NULL)
			)
		)`, id, now.Add(-s.cfg.Resend), now).Scan(&suppressed)
		if err != nil {
			return err
		}
		if suppressed {
			return nil
		}
	} else if err != nil {
		return err
	}
	_, err = s.enqueueEmail(ctx, tx, id, hash, raw, "registration", requestID, now)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(event_type,merchant_id,actor_type,request_id)
		VALUES ('merchant.registration',$1,'anonymous',$2),('email.verification_requested',$1,'system',$2)`, id, requestID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.wakeEmailDelivery()
	return nil
}

func (s *Service) validateDomain(domain string) error {
	for _, denied := range s.cfg.Denylist {
		if domain == denied {
			return ErrInvalid
		}
	}
	if len(s.cfg.Allowlist) > 0 {
		for _, allowed := range s.cfg.Allowlist {
			if domain == allowed {
				return nil
			}
		}
		return ErrInvalid
	}
	if s.cfg.RestrictFree {
		switch domain {
		case "gmail.com", "outlook.com", "hotmail.com", "yahoo.com", "icloud.com", "proton.me", "protonmail.com":
			return ErrInvalid
		}
	}
	if s.cfg.BlockDisposable {
		switch domain {
		case "mailinator.com", "guerrillamail.com", "10minutemail.com", "tempmail.com":
			return ErrInvalid
		}
	}
	return nil
}

// RequestAdminLink sends the same one-time, hashed token used by onboarding to
// an existing merchant. The response stays deliberately generic so this cannot
// be used to enumerate merchant email addresses.
func (s *Service) RequestAdminLink(ctx context.Context, value, requestID string) error {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) > 320 {
		return ErrInvalid
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || strings.Count(value, "@") != 1 {
		return ErrInvalid
	}
	if err := s.validateDomain(strings.SplitN(value, "@", 2)[1]); err != nil {
		return err
	}
	raw, hash, err := email.NewVerificationToken()
	if err != nil {
		return err
	}
	now := s.now().UTC()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	err = tx.QueryRow(ctx, `SELECT id FROM merchants WHERE business_email=$1 AND status <> 'rejected' FOR UPDATE`, value).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var suppressed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM email_verification_tokens token
		LEFT JOIN email_delivery_outbox outbox ON outbox.token_id=token.id
		WHERE token.merchant_id=$1 AND (
			(token.sent_at IS NOT NULL AND token.sent_at>$2) OR
			(token.sent_at IS NULL AND token.expires_at>$3 AND outbox.delivered_at IS NULL
			 AND outbox.abandoned_at IS NULL)
		)
	)`, id, now.Add(-s.cfg.Resend), now).Scan(&suppressed); err != nil {
		return err
	}
	if suppressed {
		return nil
	}
	_, err = s.enqueueEmail(ctx, tx, id, hash, raw, "admin_login", requestID, now)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(event_type,merchant_id,actor_type,request_id)
		VALUES ('merchant.admin_link_requested',$1,'anonymous',$2)`, id, requestID); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	s.wakeEmailDelivery()
	return nil
}

func (s *Service) VerifyEmail(ctx context.Context, token, requestID string) (string, error) {
	id, _, err := s.verifyEmail(ctx, token, requestID, false)
	return id, err
}

func (s *Service) VerifyEmailForAdmin(ctx context.Context, token, requestID string) (string, AdminSession, error) {
	return s.verifyEmail(ctx, token, requestID, true)
}

func (s *Service) verifyEmail(ctx context.Context, token, requestID string, issueSession bool) (string, AdminSession, error) {
	if len(token) < 20 {
		return "", AdminSession{}, ErrInvalid
	}
	var session AdminSession
	var err error
	if issueSession {
		session, err = s.newAdminSession()
		if err != nil {
			return "", AdminSession{}, err
		}
	}
	hash := secret.Hash(token)
	now := s.now().UTC()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", AdminSession{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	err = tx.QueryRow(ctx, `UPDATE email_verification_tokens SET consumed_at=$2
		WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>$2 RETURNING merchant_id`, hash, now).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", AdminSession{}, ErrInvalid
	}
	if err != nil {
		return "", AdminSession{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE merchants SET email_verified_at=coalesce(email_verified_at,$2),updated_at=$2 WHERE id=$1`, id, now); err != nil {
		return "", AdminSession{}, err
	}
	if issueSession {
		if _, err = tx.Exec(ctx, `INSERT INTO merchant_admin_sessions
			(merchant_id,token_hash,expires_at,created_at) VALUES ($1,$2,$3,$4)`,
			id, secret.Hash(session.Token), session.ExpiresAt, now); err != nil {
			return "", AdminSession{}, err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(event_type,merchant_id,actor_type,request_id)
		VALUES ('email.verification_completed',$1,'anonymous',$2)`, id, requestID); err != nil {
		return "", AdminSession{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", AdminSession{}, err
	}
	return id, session, nil
}

func (s *Service) WalletChallenge(ctx context.Context, merchantID, address, action, requestID string) (Challenge, error) {
	if !validUUID(merchantID) || (action != "verify-recipient" && action != "change-recipient" && action != "authenticate-admin") {
		return Challenge{}, ErrInvalid
	}
	var storedAddress, status string
	var emailVerified *time.Time
	err := s.pool.QueryRow(ctx, `SELECT recipient_address,status,email_verified_at FROM merchants WHERE id=$1`, merchantID).Scan(&storedAddress, &status, &emailVerified)
	if errors.Is(err, pgx.ErrNoRows) {
		return Challenge{}, ErrNotFound
	}
	if err != nil {
		return Challenge{}, err
	}
	if emailVerified == nil || status == "suspended" || status == "rejected" {
		return Challenge{}, ErrForbidden
	}
	if action == "verify-recipient" || action == "authenticate-admin" {
		address = storedAddress
	} else if strings.EqualFold(address, storedAddress) {
		return Challenge{}, ErrConflict
	}
	c, err := walletproof.NewChallenge(merchantID, address, action, s.now(), s.cfg.WalletTTL)
	if err != nil {
		return Challenge{}, ErrInvalid
	}
	message := c.Message()
	dbAction := strings.ReplaceAll(action, "-", "_")
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Challenge{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	id, err := persistWalletChallenge(ctx, tx, merchantID, c, message, dbAction, requestID)
	if err != nil {
		return Challenge{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Challenge{}, err
	}
	return Challenge{ID: id, Message: message, Address: c.Address, Action: action, ExpiresAt: c.ExpiresAt}, nil
}

// AuthenticatedWalletChallenge creates an API-key recipient-change challenge
// in the same transaction that revalidates the key and current active status.
// This prevents a request admitted before suspension from leaving new durable
// challenge/audit state after suspension.
func (s *Service) AuthenticatedWalletChallenge(ctx context.Context, merchantID, apiKey, address, action, requestID string) (Challenge, error) {
	if !validUUID(merchantID) || action != "change-recipient" {
		return Challenge{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Challenge{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = s.lockAuthenticatedAPIKey(ctx, tx, merchantID, apiKey); err != nil {
		return Challenge{}, err
	}
	var storedAddress, status string
	var emailVerified *time.Time
	if err = tx.QueryRow(ctx, `SELECT recipient_address,status,email_verified_at FROM merchants WHERE id=$1`, merchantID).
		Scan(&storedAddress, &status, &emailVerified); err != nil {
		return Challenge{}, err
	}
	if status != "active" || emailVerified == nil {
		return Challenge{}, ErrForbidden
	}
	if strings.EqualFold(address, storedAddress) {
		return Challenge{}, ErrConflict
	}
	c, err := walletproof.NewChallenge(merchantID, address, action, s.now(), s.cfg.WalletTTL)
	if err != nil {
		return Challenge{}, ErrInvalid
	}
	message := c.Message()
	id, err := persistWalletChallenge(ctx, tx, merchantID, c, message, "change_recipient", requestID)
	if err != nil {
		return Challenge{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Challenge{}, err
	}
	return Challenge{ID: id, Message: message, Address: c.Address, Action: action, ExpiresAt: c.ExpiresAt}, nil
}

// PendingRecipientChallenge lets an email-authenticated merchant replace an
// unverified recipient before activation. Updating the pending record first is
// safe because pending merchants cannot receive settlements, and it makes every
// older verify-recipient challenge fail the address equality check in
// VerifyWallet. Activation still requires a valid signature by the new wallet.
func (s *Service) PendingRecipientChallenge(ctx context.Context, merchantID, address, requestID string) (Challenge, error) {
	if !validUUID(merchantID) {
		return Challenge{}, ErrInvalid
	}
	normalized, err := walletproof.NormalizeAddress(address)
	if err != nil {
		return Challenge{}, ErrInvalid
	}
	now := s.now().UTC()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Challenge{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var storedAddress, status string
	var emailVerified, walletVerified *time.Time
	if err = tx.QueryRow(ctx, `SELECT recipient_address,status,email_verified_at,wallet_verified_at
		FROM merchants WHERE id=$1 FOR UPDATE`, merchantID).
		Scan(&storedAddress, &status, &emailVerified, &walletVerified); errors.Is(err, pgx.ErrNoRows) {
		return Challenge{}, ErrNotFound
	} else if err != nil {
		return Challenge{}, err
	}
	if status != "pending" || emailVerified == nil || walletVerified != nil {
		return Challenge{}, ErrForbidden
	}
	c, err := walletproof.NewChallenge(merchantID, normalized, "verify-recipient", now, s.cfg.WalletTTL)
	if err != nil {
		return Challenge{}, ErrInvalid
	}
	changed := !strings.EqualFold(storedAddress, normalized)
	if changed {
		tag, updateErr := tx.Exec(ctx, `UPDATE merchants SET recipient_address=$2,updated_at=$3
			WHERE id=$1 AND status='pending' AND email_verified_at IS NOT NULL AND wallet_verified_at IS NULL`,
			merchantID, normalized, now)
		if updateErr != nil {
			return Challenge{}, updateErr
		}
		if tag.RowsAffected() != 1 {
			return Challenge{}, ErrConflict
		}
		if _, err = tx.Exec(ctx, `INSERT INTO audit_events(event_type,merchant_id,actor_type,actor_id,request_id)
			VALUES ('recipient.pending_changed',$1,'merchant',$2,$3)`, merchantID, merchantID, requestID); err != nil {
			return Challenge{}, err
		}
	}
	message := c.Message()
	id, err := persistWalletChallenge(ctx, tx, merchantID, c, message, "verify_recipient", requestID)
	if err != nil {
		return Challenge{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Challenge{}, err
	}
	if changed && s.recipientObserver != nil {
		s.recipientObserver.ObserveRecipientChange(true)
	}
	return Challenge{ID: id, Message: message, Address: c.Address, Action: c.Action, ExpiresAt: c.ExpiresAt}, nil
}

func persistWalletChallenge(ctx context.Context, tx pgx.Tx, merchantID string, c walletproof.Challenge, message, dbAction, requestID string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO wallet_verification_challenges
		(merchant_id,address,nonce,message_hash,action,issued_at,expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		merchantID, strings.ToLower(c.Address), c.Nonce, secret.Hash(message), dbAction, c.IssuedAt, c.ExpiresAt).Scan(&id)
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(event_type,merchant_id,actor_type,request_id,metadata)
		VALUES ('wallet.challenge_created',$1,'merchant',$2,jsonb_build_object('action',$3::text))`, merchantID, requestID, dbAction); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Service) VerifyWallet(ctx context.Context, merchantID, challengeID, message, signature, expectedAction, requestID string) (string, error) {
	return s.verifyWallet(ctx, merchantID, "", challengeID, message, signature, expectedAction, requestID)
}

func (s *Service) VerifyAuthenticatedWallet(ctx context.Context, merchantID, apiKey, challengeID, message, signature, expectedAction, requestID string) (string, error) {
	return s.verifyWallet(ctx, merchantID, apiKey, challengeID, message, signature, expectedAction, requestID)
}

func (s *Service) verifyWallet(ctx context.Context, merchantID, apiKey, challengeID, message, signature, expectedAction, requestID string) (string, error) {
	if !validUUID(merchantID) || !validUUID(challengeID) || len(message) == 0 ||
		len(message) > 4096 || len(signature) != 132 {
		return "", ErrInvalid
	}
	generated, err := auth.GenerateAPIKey(s.cfg.Pepper)
	if err != nil {
		return "", err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var address, action, messageHash string
	var expires, requested time.Time
	var consumed *time.Time
	err = tx.QueryRow(ctx, `SELECT address,action,message_hash,expires_at,created_at,consumed_at
		FROM wallet_verification_challenges WHERE id=$1 AND merchant_id=$2 FOR UPDATE`, challengeID, merchantID).
		Scan(&address, &action, &messageHash, &expires, &requested, &consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if consumed != nil || !hmac.Equal([]byte(messageHash), []byte(secret.Hash(message))) {
		return "", ErrInvalid
	}
	actionWire := strings.ReplaceAll(action, "_", "-")
	if actionWire != expectedAction || (action != "verify_recipient" && action != "change_recipient") {
		return "", ErrForbidden
	}
	var currentAddress, merchantStatus string
	var emailVerified, merchantWalletVerified *time.Time
	if err = tx.QueryRow(ctx, `SELECT recipient_address,status,email_verified_at,wallet_verified_at
		FROM merchants WHERE id=$1 FOR UPDATE`, merchantID).
		Scan(&currentAddress, &merchantStatus, &emailVerified, &merchantWalletVerified); errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	if apiKey != "" {
		if expectedAction != "change-recipient" {
			return "", ErrForbidden
		}
		if err = s.lockAuthenticatedAPIKey(ctx, tx, merchantID, apiKey); err != nil {
			return "", err
		}
	}
	now := s.now().UTC()
	if !now.Before(expires) {
		return "", ErrInvalid
	}
	if err = walletproof.VerifyMessage(message, signature, merchantID, address, actionWire, now); err != nil {
		return "", ErrInvalid
	}
	if action == "verify_recipient" {
		if merchantStatus != "pending" || emailVerified == nil || merchantWalletVerified != nil ||
			!strings.EqualFold(currentAddress, address) {
			return "", ErrForbidden
		}
	} else {
		if merchantStatus != "active" || merchantWalletVerified == nil {
			return "", ErrForbidden
		}
		if strings.EqualFold(currentAddress, address) {
			return "", ErrConflict
		}
		// The cooldown is anchored to the last recipient proof rather than to
		// merchants.updated_at, which unrelated writes such as operator
		// reinstatement would otherwise push forward.
		var lastChange time.Time
		if err = tx.QueryRow(ctx,
			`SELECT coalesce(max(verified_at),'-infinity') FROM recipient_address_history WHERE merchant_id=$1`,
			merchantID).Scan(&lastChange); err != nil {
			return "", err
		}
		if now.Sub(lastChange) < s.cfg.RecipientCooldown {
			return "", ErrThrottled
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE wallet_verification_challenges SET consumed_at=$2
		WHERE id=$1 AND consumed_at IS NULL`, challengeID, now)
	if err != nil || tag.RowsAffected() != 1 {
		return "", ErrConflict
	}
	if action == "verify_recipient" {
		if tag, err = tx.Exec(ctx, `UPDATE merchants SET wallet_verified_at=$2,status='active',updated_at=$2
			WHERE id=$1 AND email_verified_at IS NOT NULL AND wallet_verified_at IS NULL
			  AND status='pending' AND recipient_address=$3`,
			merchantID, now, strings.ToLower(address)); err != nil {
			return "", err
		}
		if tag.RowsAffected() != 1 {
			return "", ErrForbidden
		}
		if _, err = tx.Exec(ctx, `INSERT INTO recipient_address_history
			(merchant_id,new_address,requested_at,verified_at,actor_type,actor_id,wallet_challenge_id)
			VALUES ($1,$2,$3,$3,'merchant',$4,$5)`, merchantID, strings.ToLower(address), now, merchantID, challengeID); err != nil {
			return "", err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO api_keys(merchant_id,name,key_prefix,key_hash) VALUES ($1,'initial',$2,$3)`,
			merchantID, generated.Prefix, generated.Hash); err != nil {
			return "", err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO audit_events(event_type,merchant_id,actor_type,actor_id,request_id)
			VALUES ('api_key.created',$1,'merchant',$2,$3)`, merchantID, merchantID, requestID); err != nil {
			return "", err
		}
	} else {
		if _, err = tx.Exec(ctx, `UPDATE merchants SET recipient_address=$2,wallet_verified_at=$3,updated_at=$3 WHERE id=$1`, merchantID, strings.ToLower(address), now); err != nil {
			return "", err
		}
		if _, err = tx.Exec(ctx, `UPDATE merchant_admin_sessions SET wallet_verified_at=NULL
			WHERE merchant_id=$1 AND wallet_verified_at IS NOT NULL`, merchantID); err != nil {
			return "", err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO recipient_address_history
			(merchant_id,previous_address,new_address,requested_at,verified_at,actor_type,actor_id,wallet_challenge_id)
			VALUES ($1,$2,$3,$4,$5,'merchant',$6,$7)`, merchantID, currentAddress, strings.ToLower(address), requested, now, merchantID, challengeID); err != nil {
			return "", err
		}
		generated.FullValue = ""
	}
	event := "wallet.verification_completed"
	if action == "change_recipient" {
		event = "recipient.changed"
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(event_type,merchant_id,actor_type,actor_id,request_id)
		VALUES ($1,$2,'merchant',$3,$4)`, event, merchantID, merchantID, requestID); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	if action == "change_recipient" && s.recipientObserver != nil {
		s.recipientObserver.ObserveRecipientChange(false)
	}
	return generated.FullValue, nil
}

func (s *Service) Authenticate(ctx context.Context, value string) (Merchant, error) {
	if !strings.HasPrefix(value, "eth402_live_") || len(value) > 256 {
		return Merchant{}, ErrUnauthorized
	}
	prefix := auth.LookupPrefix(value)
	computed := secret.KeyedHash(s.cfg.Pepper, value)
	var m Merchant
	var stored string
	err := s.pool.QueryRow(ctx, `SELECT m.id,m.name,m.business_email,m.recipient_address,m.status,m.website,m.description,
		m.email_verified_at,m.wallet_verified_at,m.stats_opted_in_at,m.public_profile_opted_in_at,m.created_at,k.key_hash
		FROM api_keys k JOIN merchants m ON m.id=k.merchant_id
		WHERE k.key_prefix=$1 AND k.revoked_at IS NULL`, prefix).Scan(
		&m.ID, &m.Name, &m.Email, &m.Recipient, &m.Status, &m.Website, &m.Description,
		&m.EmailVerifiedAt, &m.WalletVerifiedAt, &m.StatsOptedInAt, &m.PublicProfileOptedInAt, &m.CreatedAt, &stored)
	if err != nil {
		return Merchant{}, ErrUnauthorized
	}
	a, err1 := hex.DecodeString(stored)
	b, err2 := hex.DecodeString(computed)
	if err1 != nil || err2 != nil || !hmac.Equal(a, b) {
		return Merchant{}, ErrUnauthorized
	}
	if m.Status != "active" {
		return Merchant{}, ErrForbidden
	}
	_, _ = s.pool.Exec(ctx, `UPDATE api_keys SET last_used_at=now() WHERE key_prefix=$1`, prefix)
	return m, nil
}

// lockAuthenticatedAPIKey serializes an API-key-authorized operation with
// suspension. Callers must hold the transaction through the protected work.
// Keys remain stored while suspended and work again after reinstatement, but no
// request admitted against an earlier active snapshot can commit after the
// suspension update.
func (s *Service) lockAuthenticatedAPIKey(ctx context.Context, tx pgx.Tx, merchantID, value string) error {
	if !validUUID(merchantID) || !strings.HasPrefix(value, "eth402_live_") || len(value) > 256 {
		return ErrUnauthorized
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM merchants WHERE id=$1 FOR UPDATE`, merchantID).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return ErrUnauthorized
	} else if err != nil {
		return err
	}
	if status != "active" {
		return ErrForbidden
	}
	prefix := auth.LookupPrefix(value)
	computed := secret.KeyedHash(s.cfg.Pepper, value)
	var stored string
	err := tx.QueryRow(ctx, `SELECT key_hash FROM api_keys
		WHERE merchant_id=$1 AND key_prefix=$2 AND revoked_at IS NULL FOR UPDATE`, merchantID, prefix).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUnauthorized
	}
	if err != nil {
		return err
	}
	a, err1 := hex.DecodeString(stored)
	b, err2 := hex.DecodeString(computed)
	if err1 != nil || err2 != nil || !hmac.Equal(a, b) {
		return ErrUnauthorized
	}
	return nil
}

func (s *Service) CreateAPIKey(ctx context.Context, merchantID, name, requestID string) (APIKey, string, error) {
	return s.createAPIKey(ctx, merchantID, "", "", name, requestID)
}

func (s *Service) CreateAdminAPIKey(ctx context.Context, merchantID, sessionToken, name, requestID string) (APIKey, string, error) {
	return s.createAPIKey(ctx, merchantID, sessionToken, "", name, requestID)
}

func (s *Service) CreateAuthenticatedAPIKey(ctx context.Context, merchantID, apiKey, name, requestID string) (APIKey, string, error) {
	return s.createAPIKey(ctx, merchantID, "", apiKey, name, requestID)
}

func (s *Service) createAPIKey(ctx context.Context, merchantID, sessionToken, apiKey, name, requestID string) (APIKey, string, error) {
	name = strings.TrimSpace(name)
	if len(name) < 1 || len(name) > 100 {
		return APIKey{}, "", ErrInvalid
	}
	g, err := auth.GenerateAPIKey(s.cfg.Pepper)
	if err != nil {
		return APIKey{}, "", err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return APIKey{}, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if sessionToken != "" {
		if err = s.lockWalletAuthenticatedAdmin(ctx, tx, merchantID, sessionToken); err != nil {
			return APIKey{}, "", err
		}
	} else if apiKey != "" {
		if err = s.lockAuthenticatedAPIKey(ctx, tx, merchantID, apiKey); err != nil {
			return APIKey{}, "", err
		}
	}
	var key APIKey
	err = tx.QueryRow(ctx, `INSERT INTO api_keys(merchant_id,name,key_prefix,key_hash) VALUES ($1,$2,$3,$4)
		RETURNING id,name,key_prefix,created_at,last_used_at,revoked_at`, merchantID, name, g.Prefix, g.Hash).
		Scan(&key.ID, &key.Name, &key.Prefix, &key.CreatedAt, &key.LastUsedAt, &key.RevokedAt)
	if err != nil {
		return APIKey{}, "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(event_type,merchant_id,actor_type,actor_id,request_id)
		VALUES ('api_key.created',$1,'merchant',$2,$3)`, merchantID, merchantID, requestID); err != nil {
		return APIKey{}, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return APIKey{}, "", err
	}
	return key, g.FullValue, nil
}

func (s *Service) ListAPIKeys(ctx context.Context, merchantID string) ([]APIKey, error) {
	return listAPIKeys(ctx, s.pool, merchantID)
}

func (s *Service) ListAdminAPIKeys(ctx context.Context, merchantID, sessionToken string) ([]APIKey, error) {
	return s.listProtectedAPIKeys(ctx, merchantID, sessionToken, "")
}

func (s *Service) ListAuthenticatedAPIKeys(ctx context.Context, merchantID, apiKey string) ([]APIKey, error) {
	return s.listProtectedAPIKeys(ctx, merchantID, "", apiKey)
}

func (s *Service) listProtectedAPIKeys(ctx context.Context, merchantID, sessionToken, apiKey string) ([]APIKey, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if sessionToken != "" {
		if err = s.lockWalletAuthenticatedAdmin(ctx, tx, merchantID, sessionToken); err != nil {
			return nil, err
		}
	}
	if apiKey != "" {
		if err = s.lockAuthenticatedAPIKey(ctx, tx, merchantID, apiKey); err != nil {
			return nil, err
		}
	}
	result, err := listAPIKeys(ctx, tx, merchantID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

type apiKeyQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func listAPIKeys(ctx context.Context, queries apiKeyQuerier, merchantID string) ([]APIKey, error) {
	rows, err := queries.Query(ctx, `SELECT id,name,key_prefix,created_at,last_used_at,revoked_at FROM api_keys
		WHERE merchant_id=$1 ORDER BY created_at DESC`, merchantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.CreatedAt, &k.LastUsedAt, &k.RevokedAt); err != nil {
			return nil, err
		}
		result = append(result, k)
	}
	return result, rows.Err()
}

func (s *Service) RevokeAPIKey(ctx context.Context, merchantID, keyID, requestID string) error {
	return s.revokeAPIKey(ctx, merchantID, "", "", keyID, requestID)
}

func (s *Service) RevokeAdminAPIKey(ctx context.Context, merchantID, sessionToken, keyID, requestID string) error {
	return s.revokeAPIKey(ctx, merchantID, sessionToken, "", keyID, requestID)
}

func (s *Service) RevokeAuthenticatedAPIKey(ctx context.Context, merchantID, apiKey, keyID, requestID string) error {
	return s.revokeAPIKey(ctx, merchantID, "", apiKey, keyID, requestID)
}

func (s *Service) revokeAPIKey(ctx context.Context, merchantID, sessionToken, apiKey, keyID, requestID string) error {
	if !validUUID(merchantID) || !validUUID(keyID) {
		return ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if sessionToken != "" {
		if err = s.lockWalletAuthenticatedAdmin(ctx, tx, merchantID, sessionToken); err != nil {
			return err
		}
	} else if apiKey != "" {
		if err = s.lockAuthenticatedAPIKey(ctx, tx, merchantID, apiKey); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE api_keys SET revoked_at=now() WHERE id=$1 AND merchant_id=$2 AND revoked_at IS NULL`, keyID, merchantID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(event_type,merchant_id,actor_type,actor_id,request_id)
		VALUES ('api_key.revoked',$1,'merchant',$2,$3)`, merchantID, merchantID, requestID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Suspend(ctx context.Context, merchantID, reason, operator string, reinstate bool, requestID string) error {
	if !validUUID(merchantID) || len(reason) > 200 || operator == "" {
		return ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if reinstate {
		tag, err := tx.Exec(ctx, `UPDATE merchant_suspensions SET reinstated_at=now(),reinstated_by=$2
			WHERE merchant_id=$1 AND reinstated_at IS NULL`, merchantID, operator)
		if err != nil || tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		_, err = tx.Exec(ctx, `UPDATE merchants SET status='active',updated_at=now() WHERE id=$1`, merchantID)
		if err != nil {
			return err
		}
	} else {
		if strings.TrimSpace(reason) == "" {
			return ErrInvalid
		}
		tag, err := tx.Exec(ctx, `INSERT INTO merchant_suspensions(merchant_id,reason_code,operator_id)
			SELECT id,$2,$3 FROM merchants WHERE id=$1 AND status='active'`, merchantID, reason, operator)
		if err != nil {
			return ErrConflict
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		_, err = tx.Exec(ctx, `UPDATE merchants SET status='suspended',updated_at=now() WHERE id=$1`, merchantID)
		if err != nil {
			return err
		}
	}
	event := "merchant.suspended"
	if reinstate {
		event = "merchant.reinstated"
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(event_type,merchant_id,actor_type,actor_id,request_id) VALUES ($1,$2,'operator',$3,$4)`, event, merchantID, operator, requestID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) String() string {
	return fmt.Sprintf("merchant service terms=%s", s.cfg.TermsVersion)
}

func validUUID(value string) bool {
	var id pgtype.UUID
	return id.Scan(value) == nil && id.Valid
}
