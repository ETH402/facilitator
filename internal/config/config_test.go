package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		Environment: "test", HTTPAddr: ":0", PublicBaseURL: "http://localhost",
		DatabaseURL: "postgres://example", DatabaseMaxConns: 1,
		EthereumRPCURL: "http://localhost:8545", ChainID: 1, Network: MainnetNetwork,
		USDCContract: MainnetUSDC, RPCTimeout: 1, Confirmations: 1,
		MaxFeePerGasWei: "0", MaxPriorityFeeWei: "0",
		SignerMode: "disabled", EmailBackend: "log", EmailTokenTTL: 1,
		EmailResend: 1, WalletChallengeTTL: 1, MerchantSessionTTL: time.Hour, WorkerInterval: 1,
		SettlementExpiryMargin: 1, SigningTimeout: 1, SettlementLeaseDuration: 1,
		SettlementRecoveryGrace: 1, SettlementReplacementAfter: 1, SettleResponseWait: 1,
		PublicRatePerMin: 1, RegistrationRate: 1,
		MerchantSettlementQuota: 10, GlobalSettlementQuota: 100, MerchantQuotaWindow: time.Hour,
		PaymentRetention: 30 * 24 * time.Hour, EphemeralRetention: 24 * time.Hour,
		RevokedKeyRetention: 30 * 24 * time.Hour, RetentionInterval: time.Hour,
		RetentionBatchSize: 500,
		MetricsEnabled:     true,
		LogLevel:           "info",
		TermsVersion:       "test", APIKeyPepper: "01234567890123456789012345678901",
		EmailOutboxKey: "1111111111111111111111111111111111111111111111111111111111111111",
	}
}

func validProductionConfig() Config {
	cfg := validConfig()
	cfg.Environment = "production"
	cfg.PublicBaseURL = "https://eth402.example"
	cfg.DatabaseURL = "postgres://eth402@example.internal/eth402?sslmode=verify-full"
	cfg.EthereumRPCURL = "https://primary-rpc.example"
	cfg.FallbackRPCURL = "https://fallback-rpc.example"
	cfg.EmailBackend = "smtp"
	cfg.SMTPAddress = "smtp.example.com:465"
	cfg.SMTPFrom = "verify@example.com"
	cfg.SMTPTLSMode = "tls"
	cfg.SMTPTimeout = 10 * time.Second
	cfg.APIKeyPepper = "independent-production-pepper-value"
	return cfg
}

func TestLogLevel(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.LogLevel = "debug"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("debug log level rejected: %v", err)
	}
	if got := cfg.SlogLevel().String(); got != "DEBUG" {
		t.Fatalf("runtime log level = %q, want DEBUG", got)
	}
	cfg.LogLevel = "verbose"
	if err := cfg.Validate(); err == nil {
		t.Fatal("unknown log level accepted")
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

func TestEmailOutboxKeyValidation(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "abcd", strings.Repeat("z", 64), strings.Repeat("1", 62)} {
		cfg := validConfig()
		cfg.EmailOutboxKey = value
		if err := cfg.Validate(); err == nil {
			t.Fatalf("invalid email outbox key %q accepted", value)
		}
	}
	cfg := validConfig()
	cfg.APIKeyPepper = cfg.EmailOutboxKey
	if err := cfg.Validate(); err == nil {
		t.Fatal("email outbox key reused as API key pepper")
	}
}

func TestProductionRejectsDevelopmentEmailOutboxKey(t *testing.T) {
	t.Parallel()
	cfg := validProductionConfig()
	cfg.EmailOutboxKey = strings.Repeat("0", 64)
	if err := cfg.Validate(); err == nil {
		t.Fatal("production accepted development email outbox key")
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

// A gas limit below the floor is not a tuning choice: transferWithAuthorization
// costs well above 21k gas, so a lower limit guarantees every settlement
// transaction runs out of gas while still paying for the revert.
func TestGasLimitFloor(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.SignerMode = "development"
	cfg.DevSignerKey = "development-only-placeholder"
	cfg.MaxFeePerGasWei = "30000000000"
	cfg.MaxGasLimit = 21000
	if err := cfg.Validate(); err == nil {
		t.Fatal("gas limit below the floor accepted")
	}
	cfg.MaxGasLimit = MinGasLimit
	if err := cfg.Validate(); err != nil {
		t.Fatalf("gas limit at the floor rejected: %v", err)
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

func TestPaymentRetentionCoversQuotaWindow(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.PaymentRetention = 30 * time.Minute
	if err := cfg.Validate(); err == nil {
		t.Fatal("payment retention shorter than the settlement quota window accepted")
	}
	cfg.PaymentRetention = cfg.MerchantQuotaWindow
	if err := cfg.Validate(); err != nil {
		t.Fatalf("retention equal to quota window rejected: %v", err)
	}
}

func TestUnknownEmailBackendRejected(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.EmailBackend = "carrier-pigeon"
	if err := cfg.Validate(); err == nil {
		t.Fatal("unimplemented email backend accepted")
	}
}

func TestSMTPEmailBackend(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.EmailBackend = "smtp"
	cfg.SMTPAddress = "smtp.example.com:587"
	cfg.SMTPUsername = "merchant-mail"
	cfg.SMTPPassword = "secret"
	cfg.SMTPFrom = "ETH402 <verify@example.com>"
	cfg.SMTPTLSMode = "starttls"
	cfg.SMTPTimeout = 10 * time.Second
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid SMTP configuration rejected: %v", err)
	}
	cfg.SMTPPassword = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("unpaired SMTP credential accepted")
	}
	cfg.SMTPPassword = "secret"
	cfg.SMTPTLSMode = "optional"
	if err := cfg.Validate(); err == nil {
		t.Fatal("optional SMTP TLS accepted")
	}
}

func TestProductionAcceptsSMTP(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.Environment = "production"
	cfg.PublicBaseURL = "https://eth402.example"
	cfg.DatabaseURL = "postgres://eth402@example.internal/eth402?sslmode=verify-full"
	cfg.EthereumRPCURL = "https://primary-rpc.example"
	cfg.FallbackRPCURL = "https://fallback-rpc.example"
	cfg.EmailBackend = "smtp"
	cfg.SMTPAddress = "smtp.example.com:465"
	cfg.SMTPUsername = "production-smtp-user"
	cfg.SMTPPassword = "production-smtp-password"
	cfg.SMTPFrom = "verify@example.com"
	cfg.SMTPTLSMode = "tls"
	cfg.SMTPTimeout = 10 * time.Second
	cfg.APIKeyPepper = "independent-production-pepper-value"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("safe production SMTP configuration rejected: %v", err)
	}
	summary := fmt.Sprint(cfg.RedactedSummary())
	if strings.Contains(summary, cfg.SMTPPassword) || strings.Contains(summary, cfg.SMTPUsername) ||
		strings.Contains(summary, cfg.EmailOutboxKey) {
		t.Fatal("email credentials exposed in redacted configuration summary")
	}
}

func TestProductionRequiresIndependentEncryptedDependencies(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.Environment = "production"
	cfg.PublicBaseURL = "https://eth402.example"
	cfg.DatabaseURL = "postgres://eth402@example.internal/eth402?sslmode=verify-full"
	cfg.EthereumRPCURL = "https://primary-rpc.example"
	cfg.FallbackRPCURL = "https://fallback-rpc.example"
	cfg.EmailBackend = "smtp"
	cfg.SMTPAddress = "smtp.example.com:465"
	cfg.SMTPFrom = "verify@example.com"
	cfg.SMTPTLSMode = "tls"
	cfg.SMTPTimeout = 10 * time.Second
	cfg.APIKeyPepper = "independent-production-pepper-value"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("baseline production configuration rejected: %v", err)
	}
	cfg.FallbackRPCURL = cfg.EthereumRPCURL
	cfg.DatabaseURL = "postgres://eth402@example.internal/eth402?sslmode=require"
	cfg.MetricsEnabled = false
	if err := cfg.Validate(); err == nil {
		t.Fatal("production accepted one RPC, non-verifying database TLS, and disabled metrics")
	}
}

func TestProductionRPCsRequireDistinctCanonicalHostIdentities(t *testing.T) {
	t.Parallel()
	testCredential := strings.Repeat("credential", 2)
	tests := []struct {
		name, primary, fallback string
	}{
		{
			name:     "host casing paths and query credentials",
			primary:  fmt.Sprintf("HTTPS://user:%s@RPC.EXAMPLE/rpc?api-key=%s", testCredential, testCredential),
			fallback: fmt.Sprintf("https://other:%s@rpc.example/v2?api-key=%s", testCredential, testCredential),
		},
		{
			name:     "default port root and trailing dot",
			primary:  "https://rpc.example.:443/",
			fallback: "https://rpc.example?network=mainnet",
		},
		{
			name:     "different ports remain one host identity",
			primary:  "https://rpc.example:443",
			fallback: "https://rpc.example:8443/private",
		},
		{
			name:     "equivalent IPv6 spellings",
			primary:  "https://[2001:0db8:0:0:0:0:0:1]/rpc",
			fallback: "https://[2001:db8::1]:443?key=secret",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validProductionConfig()
			cfg.EthereumRPCURL = test.primary
			cfg.FallbackRPCURL = test.fallback
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), "distinct host identities") {
				t.Fatalf("equivalent RPC hosts accepted: %v", err)
			}
			for _, secret := range []string{testCredential, "api-key=", "key=secret"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("RPC validation error leaked endpoint credential %q: %v", secret, err)
				}
			}
		})
	}
}

func TestProductionRPCEndpointFormsAndAuthenticatedURLs(t *testing.T) {
	t.Parallel()
	cfg := validProductionConfig()
	testCredential := strings.Repeat("credential", 2)
	cfg.EthereumRPCURL = fmt.Sprintf("https://primary-user:%s@primary-rpc.example/v2/key?tenant=one", testCredential)
	cfg.FallbackRPCURL = fmt.Sprintf("https://fallback-rpc.example/rpc?api-key=%s", testCredential)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("authenticated RPC endpoints on distinct hosts rejected: %v", err)
	}

	for _, endpoint := range []string{
		"https://primary-rpc.example/rpc#ignored",
		"https://primary-rpc.example/rpc#",
		"https:///missing-host",
		"http://primary-rpc.example",
		"https://primary-rpc.example:0",
		"https://primary-rpc.example:65536",
		"https://réseau.example",
	} {
		t.Run(endpoint, func(t *testing.T) {
			invalid := validProductionConfig()
			invalid.EthereumRPCURL = endpoint
			if err := invalid.Validate(); err == nil {
				t.Fatalf("invalid production RPC endpoint accepted: %q", endpoint)
			}
		})
	}
}

func TestProductionRequiresPolicySigner(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.Environment = "production"
	cfg.PublicBaseURL = "https://eth402.example"
	cfg.DatabaseURL = "postgres://eth402@example.internal/eth402?sslmode=verify-full"
	cfg.EthereumRPCURL = "https://primary-rpc.example"
	cfg.FallbackRPCURL = "https://fallback-rpc.example"
	cfg.EmailBackend = "smtp"
	cfg.SMTPAddress = "smtp.example.com:465"
	cfg.SMTPFrom = "verify@example.com"
	cfg.SMTPTLSMode = "tls"
	cfg.SMTPTimeout = 10 * time.Second
	cfg.APIKeyPepper = "independent-production-pepper-value"
	cfg.SignerMode = "external"
	cfg.KMSKeyName = "projects/eth402/locations/europe-west1/keyRings/settlement/cryptoKeys/signer/cryptoKeyVersions/1"
	cfg.MaxFeePerGasWei = "1"
	cfg.MaxGasLimit = MinGasLimit
	if err := cfg.Validate(); err == nil {
		t.Fatal("production accepted direct KMS mode")
	}
}

func TestPolicySignerRequiresAnOriginURL(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.SignerMode = "policy"
	cfg.PolicySignerToken = "01234567890123456789012345678901"
	cfg.MaxFeePerGasWei = "1"
	cfg.MaxGasLimit = MinGasLimit
	for _, endpoint := range []string{
		"https://user:password@signer.example",
		"https://signer.example/path",
		"https://signer.example?redirect=elsewhere",
		"file:///tmp/signer",
	} {
		cfg.PolicySignerURL = endpoint
		if err := cfg.Validate(); err == nil {
			t.Errorf("accepted non-origin endpoint %q", endpoint)
		}
	}
	cfg.PolicySignerURL = "http://localhost:8081"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("development origin endpoint rejected: %v", err)
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
