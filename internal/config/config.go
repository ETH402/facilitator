package config

import (
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/mail"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	MainnetChainID = uint64(1)
	MainnetNetwork = "eip155:1"
	MainnetUSDC    = "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"
)

type Config struct {
	Environment       string
	HTTPAddr          string
	PublicBaseURL     string
	DatabaseURL       string
	DatabaseMaxConns  int32
	EthereumRPCURL    string
	FallbackRPCURL    string
	ChainID           uint64
	Network           string
	USDCContract      string
	RPCTimeout        time.Duration
	RPCReadRetries    int
	Confirmations     uint64
	MaxFeePerGasWei   string
	MaxPriorityFeeWei string
	MaxGasLimit       uint64
	SignerMode        string
	PolicySignerURL   string
	PolicySignerToken string
	DevSignerKey      string
	// KMSKeyName is the Cloud KMS key version resource (projects/…/locations/…/
	// keyRings/…/cryptoKeys/…/cryptoKeyVersions/N) used by the external signer
	// backend. Credentials come from Application Default Credentials.
	KMSKeyName         string
	AllowUnsafeSigner  bool
	EmailBackend       string
	EmailFileDir       string
	SMTPAddress        string
	SMTPUsername       string
	SMTPPassword       string
	SMTPFrom           string
	SMTPTLSMode        string
	SMTPTimeout        time.Duration
	EmailTokenTTL      time.Duration
	EmailResend        time.Duration
	WalletChallengeTTL time.Duration
	RecipientCooldown  time.Duration
	TermsVersion       string
	APIKeyPepper       string
	OperatorToken      string
	BlockDisposable    bool
	RestrictFreeEmail  bool
	EmailAllowlist     []string
	EmailDenylist      []string
	StatsCacheTTL      time.Duration
	// PublishStatsVolume opts into publishing settled-volume figures on the public
	// /stats endpoint. Off by default: a cumulative total polled over time yields
	// its own deltas, and a delta spanning one settlement is that payment's exact
	// amount, available to anyone without authentication.
	PublishStatsVolume bool
	// WorkerStaleAfter is how long a settlement worker may go without completing a
	// tick before the status page calls settlement degraded. Zero derives it from
	// WorkerInterval via StaleAfter.
	WorkerStaleAfter time.Duration
	WorkerInterval   time.Duration
	// SettlementExpiryMargin is the minimum lifetime an authorization must
	// have left before ETH402 will broadcast it (ADR-0004 decision 11).
	SettlementExpiryMargin time.Duration
	// SigningTimeout bounds a single signer call. Signing is a network round
	// trip in the settlement path, and its timeout is a failure mode distinct
	// from RPC failure (ADR-0004 decision 8).
	SigningTimeout time.Duration
	// SettlementLeaseDuration is how long a worker holds a payment before the
	// lease lapses and another worker may reclaim the work.
	SettlementLeaseDuration time.Duration
	// SettlementRecoveryGrace is how long recovery waits after an ambiguous
	// broadcast before re-broadcasting the identical transaction.
	SettlementRecoveryGrace time.Duration
	// SettlementReplacementAfter is how long a broadcast may sit pending
	// before recovery replaces it with a fee bump.
	SettlementReplacementAfter time.Duration
	// MerchantSettlementQuota bounds settlement intents per merchant per
	// MerchantQuotaWindow. Quota × MaxGasLimit × MaxFeePerGasWei is the
	// operator's worst-case gas exposure per merchant per window.
	MerchantSettlementQuota int
	// GlobalSettlementQuota bounds settlement intents across *all* merchants per
	// MerchantQuotaWindow. Without it the operator's total exposure is
	// merchants × MerchantSettlementQuota, which grows with the merchant list
	// rather than with anything the operator decided.
	GlobalSettlementQuota int
	MerchantQuotaWindow   time.Duration
	LogLevel              string
	MetricsEnabled        bool
	PublicRatePerMin      int
	RegistrationRate      int
	// MerchantRequestsPerWindow bounds authenticated requests per merchant per
	// FairUseWindow. Keyed on the merchant rather than the API key, since minting
	// keys is self-service and would otherwise multiply the allowance. Zero
	// disables the control.
	MerchantRequestsPerWindow int64
	FairUseWindow             time.Duration
	// Retention clears high-sensitivity terminal payment fields while preserving
	// tombstones for lifetime statistics and settlement idempotency.
	PaymentRetention    time.Duration
	EphemeralRetention  time.Duration
	RevokedKeyRetention time.Duration
	RetentionInterval   time.Duration
	RetentionBatchSize  int
	// TrustedProxies lists the reverse proxies permitted to assert a client
	// address through X-Forwarded-For. Empty means the direct peer is always
	// the client, which is correct only when the service is exposed directly.
	TrustedProxies []netip.Prefix
}

func Load() (Config, error) {
	var l loader
	cfg := Config{
		Environment:        l.str("ETH402_ENV", "development"),
		HTTPAddr:           l.str("ETH402_HTTP_ADDR", ":8080"),
		PublicBaseURL:      l.str("ETH402_PUBLIC_BASE_URL", "http://localhost:8080"),
		DatabaseURL:        os.Getenv("ETH402_DATABASE_URL"),
		DatabaseMaxConns:   l.int32("ETH402_DATABASE_MAX_CONNS", 10),
		EthereumRPCURL:     os.Getenv("ETH402_ETHEREUM_RPC_URL"),
		FallbackRPCURL:     os.Getenv("ETH402_ETHEREUM_FALLBACK_RPC_URL"),
		ChainID:            l.uint64("ETH402_ETHEREUM_CHAIN_ID", 1),
		Network:            l.str("ETH402_ETHEREUM_NETWORK", MainnetNetwork),
		USDCContract:       l.str("ETH402_USDC_CONTRACT", MainnetUSDC),
		RPCTimeout:         l.duration("ETH402_RPC_TIMEOUT", 5*time.Second),
		RPCReadRetries:     l.int("ETH402_RPC_READ_RETRIES", 2),
		Confirmations:      l.uint64("ETH402_REQUIRED_CONFIRMATIONS", 12),
		MaxFeePerGasWei:    l.str("ETH402_MAX_FEE_PER_GAS_WEI", "0"),
		MaxPriorityFeeWei:  l.str("ETH402_MAX_PRIORITY_FEE_PER_GAS_WEI", "0"),
		MaxGasLimit:        l.uint64("ETH402_MAX_GAS_LIMIT", 0),
		SignerMode:         l.str("ETH402_SIGNER_MODE", "disabled"),
		PolicySignerURL:    l.str("ETH402_POLICY_SIGNER_URL", ""),
		PolicySignerToken:  l.str("ETH402_POLICY_SIGNER_TOKEN", ""),
		DevSignerKey:       os.Getenv("ETH402_DEV_SIGNER_PRIVATE_KEY"),
		KMSKeyName:         l.str("ETH402_KMS_KEY_NAME", ""),
		AllowUnsafeSigner:  l.boolean("ETH402_ALLOW_UNSAFE_PRODUCTION_SIGNER", false),
		EmailBackend:       l.str("ETH402_EMAIL_BACKEND", "log"),
		EmailFileDir:       l.str("ETH402_EMAIL_FILE_DIR", "./email-outbox"),
		SMTPAddress:        l.str("ETH402_SMTP_ADDRESS", ""),
		SMTPUsername:       l.str("ETH402_SMTP_USERNAME", ""),
		SMTPPassword:       os.Getenv("ETH402_SMTP_PASSWORD"),
		SMTPFrom:           l.str("ETH402_SMTP_FROM", ""),
		SMTPTLSMode:        l.str("ETH402_SMTP_TLS_MODE", "starttls"),
		SMTPTimeout:        l.duration("ETH402_SMTP_TIMEOUT", 10*time.Second),
		EmailTokenTTL:      l.duration("ETH402_EMAIL_TOKEN_TTL", 30*time.Minute),
		EmailResend:        l.duration("ETH402_EMAIL_RESEND_INTERVAL", 2*time.Minute),
		WalletChallengeTTL: l.duration("ETH402_WALLET_CHALLENGE_TTL", 10*time.Minute),
		RecipientCooldown:  l.duration("ETH402_RECIPIENT_CHANGE_COOLDOWN", 24*time.Hour),
		TermsVersion:       l.str("ETH402_TERMS_VERSION", "2026-07-27"),
		APIKeyPepper:       l.str("ETH402_API_KEY_PEPPER", "eth402-development-pepper-change-me"),
		OperatorToken:      os.Getenv("ETH402_OPERATOR_TOKEN"),
		BlockDisposable:    l.boolean("ETH402_DISPOSABLE_EMAIL_BLOCK", true),
		RestrictFreeEmail:  l.boolean("ETH402_FREE_EMAIL_RESTRICTION", false),
		EmailAllowlist:     l.csv("ETH402_EMAIL_DOMAIN_ALLOWLIST"),
		EmailDenylist:      l.csv("ETH402_EMAIL_DOMAIN_DENYLIST"),
		StatsCacheTTL:      l.duration("ETH402_STATS_CACHE_TTL", 10*time.Second),
		PublishStatsVolume: l.boolean("ETH402_PUBLISH_STATS_VOLUME", false),
		// Zero means "derive from the worker interval"; see StaleAfter below.
		WorkerStaleAfter:           l.duration("ETH402_WORKER_STALE_AFTER", 0),
		WorkerInterval:             l.duration("ETH402_WORKER_INTERVAL", 15*time.Second),
		SettlementExpiryMargin:     l.duration("ETH402_SETTLEMENT_EXPIRY_MARGIN", time.Minute),
		SigningTimeout:             l.duration("ETH402_SIGNING_TIMEOUT", 10*time.Second),
		SettlementLeaseDuration:    l.duration("ETH402_SETTLEMENT_LEASE_DURATION", 2*time.Minute),
		SettlementRecoveryGrace:    l.duration("ETH402_SETTLEMENT_RECOVERY_GRACE", 2*time.Minute),
		SettlementReplacementAfter: l.duration("ETH402_SETTLEMENT_REPLACEMENT_AFTER", 5*time.Minute),
		MerchantSettlementQuota:    l.int("ETH402_MERCHANT_SETTLEMENT_QUOTA", 1000),
		GlobalSettlementQuota:      l.int("ETH402_GLOBAL_SETTLEMENT_QUOTA", 10_000),
		MerchantQuotaWindow:        l.duration("ETH402_MERCHANT_QUOTA_WINDOW", 24*time.Hour),
		LogLevel:                   l.str("ETH402_LOG_LEVEL", "info"),
		MetricsEnabled:             l.boolean("ETH402_METRICS_ENABLED", true),
		PublicRatePerMin:           l.int("ETH402_PUBLIC_RATE_PER_MINUTE", 60),
		RegistrationRate:           l.int("ETH402_REGISTRATION_RATE_PER_MINUTE", 5),
		MerchantRequestsPerWindow:  int64(l.int("ETH402_MERCHANT_REQUESTS_PER_WINDOW", 5_000)),
		FairUseWindow:              l.duration("ETH402_FAIR_USE_WINDOW", time.Hour),
		PaymentRetention:           l.duration("ETH402_PAYMENT_RETENTION", 30*24*time.Hour),
		EphemeralRetention:         l.duration("ETH402_EPHEMERAL_RETENTION", 24*time.Hour),
		RevokedKeyRetention:        l.duration("ETH402_REVOKED_KEY_RETENTION", 30*24*time.Hour),
		RetentionInterval:          l.duration("ETH402_RETENTION_INTERVAL", time.Hour),
		RetentionBatchSize:         l.int("ETH402_RETENTION_BATCH_SIZE", 500),
		TrustedProxies:             l.prefixes("ETH402_TRUSTED_PROXIES"),
	}
	return cfg, errors.Join(cfg.Validate(), errors.Join(l.errs...))
}

func (c Config) Validate() error {
	var errs []error
	if c.Environment != "development" && c.Environment != "test" && c.Environment != "production" {
		errs = append(errs, errors.New("environment must be development, test, or production"))
	}
	if c.HTTPAddr == "" {
		errs = append(errs, errors.New("HTTP address is required"))
	}
	if u, err := url.Parse(c.PublicBaseURL); err != nil || u.Scheme == "" || u.Host == "" ||
		(u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" ||
		u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		errs = append(errs, errors.New("public base URL must be an HTTP(S) origin without credentials, query, or path"))
	}
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("database URL is required"))
	}
	if c.EthereumRPCURL == "" {
		errs = append(errs, errors.New("ethereum RPC URL is required"))
	}
	if c.ChainID != MainnetChainID || c.Network != MainnetNetwork {
		errs = append(errs, errors.New("ETH402 supports only Ethereum mainnet (chain ID 1, eip155:1)"))
	}
	if !strings.EqualFold(c.USDCContract, MainnetUSDC) {
		errs = append(errs, errors.New("ETH402 supports only canonical Ethereum-mainnet USDC"))
	}
	if c.DatabaseMaxConns < 1 || c.RPCReadRetries < 0 || c.Confirmations < 1 {
		errs = append(errs, errors.New("database connections and confirmations must be positive; retries non-negative"))
	}
	if c.PublicRatePerMin < 1 || c.RegistrationRate < 1 {
		errs = append(errs, errors.New("rate limits must be positive"))
	}
	// Zero is not "unlimited": the quota is the only bound on how much gas an
	// admitted merchant can spend (ADR-0004 decision 9), so it must be an explicit
	// positive number. Raising it is a deliberate exposure decision.
	if c.MerchantSettlementQuota < 1 || c.MerchantQuotaWindow <= 0 {
		errs = append(errs, errors.New("merchant settlement quota and window must be positive"))
	}
	// The same reasoning one level up: the total the facilitator can spend per
	// window must be a number somebody chose, not merchants × per-merchant quota.
	if c.MerchantRequestsPerWindow < 0 || c.FairUseWindow < 0 {
		errs = append(errs, errors.New("merchant fair-use limit and window must not be negative"))
	}
	if c.MerchantRequestsPerWindow > 0 && c.FairUseWindow <= 0 {
		errs = append(errs, errors.New("a merchant fair-use limit requires a positive window"))
	}
	if c.PaymentRetention <= 0 || c.EphemeralRetention <= 0 ||
		c.RevokedKeyRetention <= 0 || c.RetentionInterval <= 0 {
		errs = append(errs, errors.New("retention periods and interval must be positive"))
	}
	if c.PaymentRetention < c.MerchantQuotaWindow {
		errs = append(errs, errors.New("payment retention must not be shorter than the merchant quota window"))
	}
	if c.RetentionBatchSize < 1 || c.RetentionBatchSize > 10_000 {
		errs = append(errs, errors.New("retention batch size must be between 1 and 10000"))
	}
	if c.GlobalSettlementQuota < 1 {
		errs = append(errs, errors.New("global settlement quota must be positive"))
	}
	if c.GlobalSettlementQuota > 0 && c.MerchantSettlementQuota > c.GlobalSettlementQuota {
		errs = append(errs, errors.New("global settlement quota must not be below the per-merchant quota"))
	}
	if c.RPCTimeout <= 0 || c.EmailTokenTTL <= 0 || c.EmailResend <= 0 || c.WalletChallengeTTL <= 0 || c.RecipientCooldown < 0 || c.StatsCacheTTL < 0 || c.WorkerInterval <= 0 {
		errs = append(errs, errors.New("durations must be positive (stats cache may be zero)"))
	}
	if c.SettlementExpiryMargin <= 0 || c.SigningTimeout <= 0 || c.SettlementLeaseDuration <= 0 {
		errs = append(errs, errors.New("settlement expiry margin, signing timeout, and lease duration must be positive"))
	}
	if c.SettlementRecoveryGrace <= 0 || c.SettlementReplacementAfter <= 0 {
		errs = append(errs, errors.New("settlement recovery grace and replacement delay must be positive"))
	}
	if len(c.APIKeyPepper) < 32 {
		errs = append(errs, errors.New("API key pepper must be at least 32 bytes"))
	}
	if c.TermsVersion == "" {
		errs = append(errs, errors.New("terms version is required"))
	}
	if c.EmailBackend != "log" && c.EmailBackend != "file" && c.EmailBackend != "smtp" {
		errs = append(errs, errors.New("email backend must be log, file, or smtp"))
	}
	if c.EmailBackend == "smtp" {
		host, _, addressErr := net.SplitHostPort(c.SMTPAddress)
		if addressErr != nil || host == "" {
			errs = append(errs, errors.New("SMTP address must be host:port"))
		}
		if (c.SMTPUsername == "") != (c.SMTPPassword == "") {
			errs = append(errs, errors.New("SMTP username and password must be configured together"))
		}
		if from, fromErr := mail.ParseAddress(c.SMTPFrom); fromErr != nil || from.Address == "" ||
			strings.ContainsAny(c.SMTPFrom, "\r\n") {
			errs = append(errs, errors.New("SMTP from address is invalid"))
		}
		if c.SMTPTLSMode != "starttls" && c.SMTPTLSMode != "tls" {
			errs = append(errs, errors.New("SMTP TLS mode must be starttls or tls"))
		}
		if c.SMTPTimeout <= 0 {
			errs = append(errs, errors.New("SMTP timeout must be positive"))
		}
	}
	maxFee, maxFeeOK := new(big.Int).SetString(c.MaxFeePerGasWei, 10)
	priorityFee, priorityFeeOK := new(big.Int).SetString(c.MaxPriorityFeeWei, 10)
	if !maxFeeOK || maxFee.Sign() < 0 {
		errs = append(errs, errors.New("max fee per gas must be an unsigned decimal integer"))
	}
	if !priorityFeeOK || priorityFee.Sign() < 0 {
		errs = append(errs, errors.New("max priority fee per gas must be an unsigned decimal integer"))
	}
	// EIP-1559 requires the priority fee to fit inside the total fee ceiling. A
	// zero ceiling means "unset" and is checked by the signer gate below.
	if maxFeeOK && priorityFeeOK && maxFee.Sign() > 0 && priorityFee.Cmp(maxFee) > 0 {
		errs = append(errs, errors.New("max priority fee per gas must not exceed max fee per gas"))
	}
	// A settlement signer must never operate without an explicit spend ceiling:
	// zero means unset, not unlimited. See docs/OPERATIONS.md.
	if c.SignerMode != "disabled" && (!maxFeeOK || maxFee.Sign() <= 0 || c.MaxGasLimit == 0) {
		errs = append(errs, errors.New("enabling a settlement signer requires non-zero max fee per gas and max gas limit"))
	}
	seenDomains := make(map[string]bool, len(c.EmailAllowlist))
	for _, domain := range c.EmailAllowlist {
		seenDomains[domain] = true
	}
	for _, domain := range c.EmailDenylist {
		if seenDomains[domain] {
			errs = append(errs, fmt.Errorf("email domain %q cannot be both allowed and denied", domain))
		}
	}
	if c.SignerMode != "disabled" && c.SignerMode != "development" &&
		c.SignerMode != "external" && c.SignerMode != "policy" {
		errs = append(errs, errors.New("signer mode must be disabled, development, external, or policy"))
	}
	if c.SignerMode == "policy" {
		if c.PolicySignerURL == "" {
			errs = append(errs, errors.New("policy signer requires ETH402_POLICY_SIGNER_URL"))
		}
		if len(c.PolicySignerToken) < 32 {
			// The token is the only thing standing between the signing boundary and
			// anything that can reach its port, so a weak one is a misconfiguration.
			errs = append(errs, errors.New("policy signer requires ETH402_POLICY_SIGNER_TOKEN of at least 32 characters"))
		}
		if c.Environment == "production" && !strings.HasPrefix(c.PolicySignerURL, "https://") {
			// Plaintext would put the bearer token and every authorization on the
			// wire. Localhost is not exempted: in production the boundary is a
			// separate workload, so a localhost URL is itself a misconfiguration.
			errs = append(errs, errors.New("production policy signer URL must use HTTPS"))
		}
	}
	if c.SignerMode == "development" && c.DevSignerKey == "" {
		errs = append(errs, errors.New("development signer requires a private key"))
	}
	if c.SignerMode == "external" && !kmsKeyNamePattern.MatchString(c.KMSKeyName) {
		errs = append(errs, errors.New("external signer requires ETH402_KMS_KEY_NAME as projects/…/locations/…/keyRings/…/cryptoKeys/…/cryptoKeyVersions/N"))
	}
	if c.Environment == "production" {
		if c.PublicBaseURL != "" && !strings.HasPrefix(c.PublicBaseURL, "https://") {
			errs = append(errs, errors.New("production public URL must use HTTPS"))
		}
		primaryRPC, primaryErr := url.Parse(c.EthereumRPCURL)
		fallbackRPC, fallbackErr := url.Parse(c.FallbackRPCURL)
		if primaryErr != nil || primaryRPC.Scheme != "https" {
			errs = append(errs, errors.New("production primary Ethereum RPC must use HTTPS"))
		}
		if c.FallbackRPCURL == "" || fallbackErr != nil || fallbackRPC.Scheme != "https" {
			errs = append(errs, errors.New("production requires an HTTPS fallback Ethereum RPC"))
		}
		if c.FallbackRPCURL != "" && c.FallbackRPCURL == c.EthereumRPCURL {
			errs = append(errs, errors.New("production Ethereum RPCs must be distinct"))
		}
		database, databaseErr := url.Parse(c.DatabaseURL)
		if databaseErr != nil || (database.Scheme != "postgres" && database.Scheme != "postgresql") ||
			database.Query().Get("sslmode") != "verify-full" {
			errs = append(errs, errors.New("production database URL must use sslmode=verify-full"))
		}
		if c.EmailBackend != "smtp" {
			errs = append(errs, errors.New("production requires the SMTP email backend"))
		}
		if c.SignerMode != "disabled" && c.SignerMode != "policy" {
			errs = append(errs, errors.New("production settlement requires the policy signer or disabled mode"))
		}
		if c.DevSignerKey != "" || c.AllowUnsafeSigner {
			errs = append(errs, errors.New("raw signer material and unsafe signer overrides are forbidden in production"))
		}
		if c.APIKeyPepper == "eth402-development-pepper-change-me" {
			errs = append(errs, errors.New("development API key pepper is forbidden in production"))
		}
		if c.OperatorToken != "" && len(c.OperatorToken) < 32 {
			errs = append(errs, errors.New("production operator token must be at least 32 bytes"))
		}
		if !c.MetricsEnabled {
			errs = append(errs, errors.New("production metrics must be enabled"))
		}
	}
	return errors.Join(errs...)
}

// kmsKeyNamePattern pins the Cloud KMS key version resource shape; signing
// always names a concrete version so rotation is an explicit config change.
var kmsKeyNamePattern = regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/keyRings/[^/]+/cryptoKeys/[^/]+/cryptoKeyVersions/[1-9][0-9]*$`)

// loader reads environment variables and accumulates parse failures so that a
// malformed value is reported by name instead of silently collapsing to a
// sentinel that Validate would either misattribute or accept.
type loader struct{ errs []error }

func (l *loader) str(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func (l *loader) reject(key, requirement string) {
	l.errs = append(l.errs, fmt.Errorf("%s must be %s", key, requirement))
}

func (l *loader) int(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		l.reject(key, "an integer")
		return fallback
	}
	return n
}

func (l *loader) int32(key string, fallback int32) int32 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	n, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		l.reject(key, "a 32-bit integer")
		return fallback
	}
	return int32(n)
}

func (l *loader) uint64(key string, fallback uint64) uint64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		l.reject(key, "an unsigned 64-bit integer")
		return fallback
	}
	return n
}

func (l *loader) boolean(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	b, err := strconv.ParseBool(value)
	if err != nil {
		l.reject(key, "a boolean")
		return fallback
	}
	return b
}

func (l *loader) duration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		l.reject(key, "a Go duration such as 30s or 24h")
		return fallback
	}
	return d
}

func (l *loader) csv(key string) []string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if normalized := strings.ToLower(strings.TrimSpace(part)); normalized != "" {
			result = append(result, normalized)
		}
	}
	return result
}

// prefixes parses trusted proxy entries given as CIDR prefixes or bare IP
// addresses. A bare address becomes a single-host prefix.
func (l *loader) prefixes(key string) []netip.Prefix {
	values := l.csv(key)
	if len(values) == 0 {
		return nil
	}
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		if prefix, err := netip.ParsePrefix(value); err == nil {
			result = append(result, prefix.Masked())
			continue
		}
		address, err := netip.ParseAddr(value)
		if err != nil {
			l.errs = append(l.errs, fmt.Errorf("%s entry %q must be an IP address or CIDR prefix", key, value))
			continue
		}
		result = append(result, netip.PrefixFrom(address, address.BitLen()))
	}
	return result
}

// StaleAfter is the worker-heartbeat deadline the status page uses. Derived from
// the worker interval by default, because a fixed default would be either
// trigger-happy or blind depending on how the interval is tuned. Four intervals
// tolerates one slow tick and a retry without reporting an outage for a service
// that is merely busy.
func (c Config) StaleAfter() time.Duration {
	if c.WorkerStaleAfter > 0 {
		return c.WorkerStaleAfter
	}
	if c.WorkerInterval > 0 {
		return 4 * c.WorkerInterval
	}
	return time.Minute
}

func (c Config) RedactedSummary() map[string]any {
	return map[string]any{
		"environment": c.Environment, "http_addr": c.HTTPAddr, "network": c.Network,
		"chain_id": c.ChainID, "usdc_contract": c.USDCContract, "signer_mode": c.SignerMode,
		"database_configured": c.DatabaseURL != "", "rpc_configured": c.EthereumRPCURL != "",
		"email_backend": c.EmailBackend, "metrics_enabled": c.MetricsEnabled,
		"trusted_proxies": len(c.TrustedProxies),
	}
}

func (c Config) String() string {
	return fmt.Sprintf("environment=%s network=%s signer=%s", c.Environment, c.Network, c.SignerMode)
}
