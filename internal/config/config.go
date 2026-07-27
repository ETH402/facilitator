package config

import (
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
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
	Environment        string
	HTTPAddr           string
	PublicBaseURL      string
	DatabaseURL        string
	DatabaseMaxConns   int32
	EthereumRPCURL     string
	FallbackRPCURL     string
	ChainID            uint64
	Network            string
	USDCContract       string
	RPCTimeout         time.Duration
	RPCReadRetries     int
	Confirmations      uint64
	MaxFeePerGasWei    string
	MaxPriorityFeeWei  string
	MaxGasLimit        uint64
	SignerMode         string
	DevSignerKey       string
	AllowUnsafeSigner  bool
	EmailBackend       string
	EmailFileDir       string
	EmailTokenTTL      time.Duration
	EmailResend        time.Duration
	WalletChallengeTTL time.Duration
	BlockDisposable    bool
	RestrictFreeEmail  bool
	EmailAllowlist     []string
	EmailDenylist      []string
	StatsCacheTTL      time.Duration
	WorkerInterval     time.Duration
	LogLevel           string
	MetricsEnabled     bool
	PublicRatePerMin   int
	RegistrationRate   int
}

func Load() (Config, error) {
	cfg := Config{
		Environment:        env("ETH402_ENV", "development"),
		HTTPAddr:           env("ETH402_HTTP_ADDR", ":8080"),
		PublicBaseURL:      env("ETH402_PUBLIC_BASE_URL", "http://localhost:8080"),
		DatabaseURL:        os.Getenv("ETH402_DATABASE_URL"),
		DatabaseMaxConns:   envInt32("ETH402_DATABASE_MAX_CONNS", 10),
		EthereumRPCURL:     os.Getenv("ETH402_ETHEREUM_RPC_URL"),
		FallbackRPCURL:     os.Getenv("ETH402_ETHEREUM_FALLBACK_RPC_URL"),
		ChainID:            envUint64("ETH402_ETHEREUM_CHAIN_ID", 1),
		Network:            env("ETH402_ETHEREUM_NETWORK", MainnetNetwork),
		USDCContract:       env("ETH402_USDC_CONTRACT", MainnetUSDC),
		RPCTimeout:         envDuration("ETH402_RPC_TIMEOUT", 5*time.Second),
		RPCReadRetries:     envInt("ETH402_RPC_READ_RETRIES", 2),
		Confirmations:      envUint64("ETH402_REQUIRED_CONFIRMATIONS", 12),
		MaxFeePerGasWei:    env("ETH402_MAX_FEE_PER_GAS_WEI", "0"),
		MaxPriorityFeeWei:  env("ETH402_MAX_PRIORITY_FEE_PER_GAS_WEI", "0"),
		MaxGasLimit:        envUint64("ETH402_MAX_GAS_LIMIT", 0),
		SignerMode:         env("ETH402_SIGNER_MODE", "disabled"),
		DevSignerKey:       os.Getenv("ETH402_DEV_SIGNER_PRIVATE_KEY"),
		AllowUnsafeSigner:  envBool("ETH402_ALLOW_UNSAFE_PRODUCTION_SIGNER", false),
		EmailBackend:       env("ETH402_EMAIL_BACKEND", "log"),
		EmailFileDir:       env("ETH402_EMAIL_FILE_DIR", "./email-outbox"),
		EmailTokenTTL:      envDuration("ETH402_EMAIL_TOKEN_TTL", 30*time.Minute),
		EmailResend:        envDuration("ETH402_EMAIL_RESEND_INTERVAL", 2*time.Minute),
		WalletChallengeTTL: envDuration("ETH402_WALLET_CHALLENGE_TTL", 10*time.Minute),
		BlockDisposable:    envBool("ETH402_DISPOSABLE_EMAIL_BLOCK", true),
		RestrictFreeEmail:  envBool("ETH402_FREE_EMAIL_RESTRICTION", false),
		EmailAllowlist:     envCSV("ETH402_EMAIL_DOMAIN_ALLOWLIST"),
		EmailDenylist:      envCSV("ETH402_EMAIL_DOMAIN_DENYLIST"),
		StatsCacheTTL:      envDuration("ETH402_STATS_CACHE_TTL", 10*time.Second),
		WorkerInterval:     envDuration("ETH402_WORKER_INTERVAL", 15*time.Second),
		LogLevel:           env("ETH402_LOG_LEVEL", "info"),
		MetricsEnabled:     envBool("ETH402_METRICS_ENABLED", true),
		PublicRatePerMin:   envInt("ETH402_PUBLIC_RATE_PER_MINUTE", 60),
		RegistrationRate:   envInt("ETH402_REGISTRATION_RATE_PER_MINUTE", 5),
	}
	var parseErrors []error
	for _, key := range []string{
		"ETH402_ALLOW_UNSAFE_PRODUCTION_SIGNER", "ETH402_METRICS_ENABLED",
		"ETH402_DISPOSABLE_EMAIL_BLOCK", "ETH402_FREE_EMAIL_RESTRICTION",
	} {
		if value := os.Getenv(key); value != "" {
			if _, err := strconv.ParseBool(value); err != nil {
				parseErrors = append(parseErrors, fmt.Errorf("%s must be a boolean", key))
			}
		}
	}
	return cfg, errors.Join(cfg.Validate(), errors.Join(parseErrors...))
}

func (c Config) Validate() error {
	var errs []error
	if c.Environment != "development" && c.Environment != "test" && c.Environment != "production" {
		errs = append(errs, errors.New("environment must be development, test, or production"))
	}
	if c.HTTPAddr == "" {
		errs = append(errs, errors.New("HTTP address is required"))
	}
	if u, err := url.Parse(c.PublicBaseURL); err != nil || u.Scheme == "" || u.Host == "" {
		errs = append(errs, errors.New("public base URL must be absolute"))
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
	if c.RPCTimeout <= 0 || c.EmailTokenTTL <= 0 || c.EmailResend <= 0 || c.WalletChallengeTTL <= 0 || c.StatsCacheTTL < 0 || c.WorkerInterval <= 0 {
		errs = append(errs, errors.New("durations must be positive (stats cache may be zero)"))
	}
	for name, value := range map[string]string{
		"max fee per gas": c.MaxFeePerGasWei, "max priority fee per gas": c.MaxPriorityFeeWei,
	} {
		n, ok := new(big.Int).SetString(value, 10)
		if !ok || n.Sign() < 0 {
			errs = append(errs, fmt.Errorf("%s must be an unsigned decimal integer", name))
		}
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
	if c.SignerMode != "disabled" && c.SignerMode != "development" && c.SignerMode != "external" {
		errs = append(errs, errors.New("signer mode must be disabled, development, or external"))
	}
	if c.SignerMode == "development" && c.DevSignerKey == "" {
		errs = append(errs, errors.New("development signer requires a private key"))
	}
	if c.Environment == "production" {
		if c.PublicBaseURL != "" && !strings.HasPrefix(c.PublicBaseURL, "https://") {
			errs = append(errs, errors.New("production public URL must use HTTPS"))
		}
		if c.EmailBackend == "log" || c.EmailBackend == "file" {
			errs = append(errs, errors.New("development email backend is forbidden in production"))
		}
		if (c.SignerMode == "development" || c.DevSignerKey != "") && !c.AllowUnsafeSigner {
			errs = append(errs, errors.New("raw development signer is forbidden in production"))
		}
	}
	return errors.Join(errs...)
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return -1
	}
	return n
}

func envInt32(key string, fallback int32) int32 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	n, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return -1
	}
	return int32(n)
}

func envUint64(key string, fallback uint64) uint64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func envBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	b, err := strconv.ParseBool(value)
	if err != nil {
		return false
	}
	return b
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return -1
	}
	return d
}

func envCSV(key string) []string {
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

func (c Config) RedactedSummary() map[string]any {
	return map[string]any{
		"environment": c.Environment, "http_addr": c.HTTPAddr, "network": c.Network,
		"chain_id": c.ChainID, "usdc_contract": c.USDCContract, "signer_mode": c.SignerMode,
		"database_configured": c.DatabaseURL != "", "rpc_configured": c.EthereumRPCURL != "",
	}
}

func (c Config) String() string {
	return fmt.Sprintf("environment=%s network=%s signer=%s", c.Environment, c.Network, c.SignerMode)
}
