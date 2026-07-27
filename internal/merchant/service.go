package merchant

import (
	"context"
	"crypto/hmac"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
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
	Pepper                                         []byte
	BlockDisposable, RestrictFree                  bool
	Allowlist, Denylist                            []string
}

type Service struct {
	pool *pgxpool.Pool
	mail email.Sender
	cfg  Config
	now  func() time.Time
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
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Email            string     `json:"business_email"`
	Recipient        string     `json:"recipient_address"`
	Status           string     `json:"status"`
	Website          *string    `json:"website,omitempty"`
	Description      *string    `json:"description,omitempty"`
	EmailVerifiedAt  *time.Time `json:"email_verified_at,omitempty"`
	WalletVerifiedAt *time.Time `json:"wallet_verified_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
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
	return &Service{pool: pool, mail: sender, cfg: cfg, now: time.Now}
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
		err = tx.QueryRow(ctx, `SELECT id FROM merchants WHERE business_email=$1 AND status <> 'rejected'`, in.Email).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			// The conflicting row was rejected between the insert and this
			// read. Stay silent so the response cannot distinguish states.
			return nil
		}
		if err != nil {
			return err
		}
		var last time.Time
		err = tx.QueryRow(ctx, `SELECT coalesce(max(sent_at),'-infinity') FROM email_verification_tokens WHERE merchant_id=$1`, id).Scan(&last)
		if err != nil {
			return err
		}
		if now.Sub(last) < s.cfg.Resend {
			return nil
		}
	} else if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO email_verification_tokens(merchant_id,token_hash,expires_at,sent_at)
		VALUES ($1,$2,$3,$4)`, id, hash, now.Add(s.cfg.EmailTTL), now); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(event_type,merchant_id,actor_type,request_id)
		VALUES ('merchant.registration',$1,'anonymous',$2),('email.verification_requested',$1,'system',$2)`, id, requestID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	link := strings.TrimRight(s.cfg.BaseURL, "/") + "/verify-email?token=" + url.QueryEscape(raw)
	return s.mail.Send(ctx, email.Message{To: in.Email, Subject: "Verify your ETH402 email", TextBody: "Verify your email: " + link})
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

func (s *Service) VerifyEmail(ctx context.Context, token, requestID string) (string, error) {
	if len(token) < 20 {
		return "", ErrInvalid
	}
	hash := secret.Hash(token)
	now := s.now().UTC()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	err = tx.QueryRow(ctx, `UPDATE email_verification_tokens SET consumed_at=$2
		WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>$2 RETURNING merchant_id`, hash, now).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrInvalid
	}
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `UPDATE merchants SET email_verified_at=coalesce(email_verified_at,$2),updated_at=$2 WHERE id=$1`, id, now); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(event_type,merchant_id,actor_type,request_id)
		VALUES ('email.verification_completed',$1,'anonymous',$2)`, id, requestID); err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

func (s *Service) WalletChallenge(ctx context.Context, merchantID, address, action, requestID string) (Challenge, error) {
	if !validUUID(merchantID) || (action != "verify-recipient" && action != "change-recipient") {
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
	if action == "verify-recipient" {
		address = storedAddress
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
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO wallet_verification_challenges
		(merchant_id,address,nonce,message_hash,action,issued_at,expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		merchantID, strings.ToLower(c.Address), c.Nonce, secret.Hash(message), dbAction, c.IssuedAt, c.ExpiresAt).Scan(&id)
	if err != nil {
		return Challenge{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(event_type,merchant_id,actor_type,request_id,metadata)
		VALUES ('wallet.challenge_created',$1,'merchant',$2,jsonb_build_object('action',$3::text))`, merchantID, requestID, dbAction)
	if err != nil {
		return Challenge{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Challenge{}, err
	}
	return Challenge{ID: id, Message: message, Address: c.Address, Action: action, ExpiresAt: c.ExpiresAt}, nil
}

func (s *Service) VerifyWallet(ctx context.Context, merchantID, challengeID, message, signature, expectedAction, requestID string) (string, error) {
	if !validUUID(merchantID) || !validUUID(challengeID) || len(message) == 0 ||
		len(message) > 4096 || len(signature) != 132 {
		return "", ErrInvalid
	}
	var address, action, messageHash string
	var expires, requested time.Time
	var consumed *time.Time
	err := s.pool.QueryRow(ctx, `SELECT address,action,message_hash,expires_at,created_at,consumed_at
		FROM wallet_verification_challenges WHERE id=$1 AND merchant_id=$2`, challengeID, merchantID).
		Scan(&address, &action, &messageHash, &expires, &requested, &consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if consumed != nil || !s.now().Before(expires) || !hmac.Equal([]byte(messageHash), []byte(secret.Hash(message))) {
		return "", ErrInvalid
	}
	actionWire := strings.ReplaceAll(action, "_", "-")
	if actionWire != expectedAction {
		return "", ErrForbidden
	}
	if err := walletproof.VerifyMessage(message, signature, merchantID, address, actionWire, s.now()); err != nil {
		return "", ErrInvalid
	}
	generated, err := auth.GenerateAPIKey(s.cfg.Pepper)
	if err != nil {
		return "", err
	}
	now := s.now().UTC()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE wallet_verification_challenges SET consumed_at=$2 WHERE id=$1 AND consumed_at IS NULL`, challengeID, now)
	if err != nil || tag.RowsAffected() != 1 {
		return "", ErrConflict
	}
	if action == "verify_recipient" {
		if tag, err = tx.Exec(ctx, `UPDATE merchants SET wallet_verified_at=$2,status='active',updated_at=$2
			WHERE id=$1 AND email_verified_at IS NOT NULL AND status='pending'`, merchantID, now); err != nil {
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
		var previous string
		if err = tx.QueryRow(ctx, `SELECT recipient_address FROM merchants WHERE id=$1 AND status='active' FOR UPDATE`, merchantID).Scan(&previous); err != nil {
			return "", ErrForbidden
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
		if _, err = tx.Exec(ctx, `UPDATE merchants SET recipient_address=$2,wallet_verified_at=$3,updated_at=$3 WHERE id=$1`, merchantID, strings.ToLower(address), now); err != nil {
			return "", err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO recipient_address_history
			(merchant_id,previous_address,new_address,requested_at,verified_at,actor_type,actor_id,wallet_challenge_id)
			VALUES ($1,$2,$3,$4,$5,'merchant',$6,$7)`, merchantID, previous, strings.ToLower(address), requested, now, merchantID, challengeID); err != nil {
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
		m.email_verified_at,m.wallet_verified_at,m.created_at,k.key_hash
		FROM api_keys k JOIN merchants m ON m.id=k.merchant_id
		WHERE k.key_prefix=$1 AND k.revoked_at IS NULL`, prefix).Scan(
		&m.ID, &m.Name, &m.Email, &m.Recipient, &m.Status, &m.Website, &m.Description,
		&m.EmailVerifiedAt, &m.WalletVerifiedAt, &m.CreatedAt, &stored)
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

func (s *Service) CreateAPIKey(ctx context.Context, merchantID, name, requestID string) (APIKey, string, error) {
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
	rows, err := s.pool.Query(ctx, `SELECT id,name,key_prefix,created_at,last_used_at,revoked_at FROM api_keys
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
	if !validUUID(merchantID) || !validUUID(keyID) {
		return ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
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
