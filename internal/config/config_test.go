package config

import (
	"strings"
	"testing"
)

func validConfig() Config {
	return Config{
		Environment: "test", HTTPAddr: ":0", PublicBaseURL: "http://localhost",
		DatabaseURL: "postgres://example", DatabaseMaxConns: 1,
		EthereumRPCURL: "http://localhost:8545", ChainID: 1, Network: MainnetNetwork,
		USDCContract: MainnetUSDC, RPCTimeout: 1, Confirmations: 1,
		MaxFeePerGasWei: "0", MaxPriorityFeeWei: "0",
		SignerMode: "disabled", EmailBackend: "log", EmailTokenTTL: 1,
		EmailResend: 1, WalletChallengeTTL: 1, WorkerInterval: 1,
		PublicRatePerMin: 1, RegistrationRate: 1,
		TermsVersion: "test", APIKeyPepper: "01234567890123456789012345678901",
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	bad := validConfig()
	bad.ChainID = 8453
	if err := bad.Validate(); err == nil {
		t.Fatal("unsupported chain accepted")
	}
	bad = validConfig()
	bad.USDCContract = "0x0000000000000000000000000000000000000000"
	if err := bad.Validate(); err == nil {
		t.Fatal("unsupported asset accepted")
	}
}

func TestProductionSafety(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.Environment = "production"
	cfg.PublicBaseURL = "http://eth402.org"
	cfg.EmailBackend = "file"
	cfg.SignerMode = "development"
	cfg.DevSignerKey = "secret"
	if err := cfg.Validate(); err == nil {
		t.Fatal("unsafe production config accepted")
	}
}

func TestUnknownEmailBackendRejected(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.EmailBackend = "smtp"
	if err := cfg.Validate(); err == nil {
		t.Fatal("unimplemented email backend accepted")
	}
}

// requiredEnv is the minimum environment Load needs to reach validation.
func requiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ETH402_ENV", "test")
	t.Setenv("ETH402_DATABASE_URL", "postgres://example")
	t.Setenv("ETH402_ETHEREUM_RPC_URL", "http://localhost:8545")
	t.Setenv("ETH402_API_KEY_PEPPER", "01234567890123456789012345678901")
}

func TestTrustedProxiesParsing(t *testing.T) {
	requiredEnv(t)
	t.Setenv("ETH402_TRUSTED_PROXIES", "10.0.0.0/8, 192.168.1.5, 2001:db8::/32")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("valid trusted proxies rejected: %v", err)
	}
	if len(cfg.TrustedProxies) != 3 {
		t.Fatalf("trusted proxies = %v, want 3 entries", cfg.TrustedProxies)
	}
	// A bare address must become a single-host prefix.
	if got := cfg.TrustedProxies[1].String(); got != "192.168.1.5/32" {
		t.Fatalf("bare address parsed as %q, want 192.168.1.5/32", got)
	}
}

func TestTrustedProxiesRejectsMalformedEntry(t *testing.T) {
	requiredEnv(t)
	t.Setenv("ETH402_TRUSTED_PROXIES", "10.0.0.0/8,not-an-address")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "not-an-address") {
		t.Fatalf("error = %v, want one naming the malformed entry", err)
	}
}

func TestTrustedProxiesDefaultsToEmpty(t *testing.T) {
	requiredEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("default config rejected: %v", err)
	}
	if len(cfg.TrustedProxies) != 0 {
		t.Fatalf("trusted proxies = %v, want none by default", cfg.TrustedProxies)
	}
}

func TestPublicBaseURLMustBeOrigin(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.PublicBaseURL = "https://eth402.org/path?token=unsafe"
	if err := cfg.Validate(); err == nil {
		t.Fatal("public base URL with path and query accepted")
	}
}
