package config

import "testing"

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
