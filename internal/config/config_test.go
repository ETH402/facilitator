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
		SettlementExpiryMargin: 1, SigningTimeout: 1, SettlementLeaseDuration: 1,
		SettlementRecoveryGrace: 1, SettlementReplacementAfter: 1,
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

func TestSignerRequiresExplicitGasCeiling(t *testing.T) {
	t.Parallel()
	// Zero means unset, so a signer must not be enablable without a ceiling.
	cfg := validConfig()
	cfg.SignerMode = "development"
	cfg.DevSignerKey = "development-only-placeholder"
	if err := cfg.Validate(); err == nil {
		t.Fatal("development signer accepted with zero gas policy")
	}
	cfg.MaxFeePerGasWei = "30000000000"
	cfg.MaxGasLimit = 120000
	if err := cfg.Validate(); err != nil {
		t.Fatalf("development signer rejected with explicit gas policy: %v", err)
	}
	// The disabled signer keeps working with the zero placeholders.
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("disabled signer rejected: %v", err)
	}
}

func TestExternalSignerRequiresKMSKeyName(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.SignerMode = "external"
	cfg.MaxFeePerGasWei = "30000000000"
	cfg.MaxGasLimit = 120000
	if err := cfg.Validate(); err == nil {
		t.Fatal("external signer accepted without a KMS key name")
	}
	cfg.KMSKeyName = "projects/eth402/locations/europe-west1/keyRings/eth402-settlement/cryptoKeys/eth402-settlement-signer/cryptoKeyVersions/1"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("external signer rejected with a valid key name: %v", err)
	}
	// Signing always names a concrete version: the bare key is not enough.
	cfg.KMSKeyName = "projects/eth402/locations/europe-west1/keyRings/eth402-settlement/cryptoKeys/eth402-settlement-signer"
	if err := cfg.Validate(); err == nil {
		t.Fatal("external signer accepted a key without a version")
	}
}

func TestPriorityFeeCannotExceedMaxFee(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.MaxFeePerGasWei = "1000"
	cfg.MaxPriorityFeeWei = "1001"
	if err := cfg.Validate(); err == nil {
		t.Fatal("priority fee above max fee accepted")
	}
	cfg.MaxPriorityFeeWei = "1000"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("priority fee equal to max fee rejected: %v", err)
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

func TestMalformedNumericValueIsReportedByName(t *testing.T) {
	requiredEnv(t)
	// A silent fallback here previously reported an unrelated validation
	// failure, or none at all when zero happened to be permitted.
	t.Setenv("ETH402_MAX_GAS_LIMIT", "not-a-number")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "ETH402_MAX_GAS_LIMIT") {
		t.Fatalf("error = %v, want one naming ETH402_MAX_GAS_LIMIT", err)
	}
}

func TestMalformedDurationIsReportedByName(t *testing.T) {
	requiredEnv(t)
	t.Setenv("ETH402_RPC_TIMEOUT", "5 seconds")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "ETH402_RPC_TIMEOUT") {
		t.Fatalf("error = %v, want one naming ETH402_RPC_TIMEOUT", err)
	}
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
