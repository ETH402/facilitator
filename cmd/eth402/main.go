package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ETH402/facilitator/internal/config"
	"github.com/ETH402/facilitator/internal/email"
	"github.com/ETH402/facilitator/internal/ethereum"
	"github.com/ETH402/facilitator/internal/httpapi"
	"github.com/ETH402/facilitator/internal/merchant"
	"github.com/ETH402/facilitator/internal/metrics"
	"github.com/ETH402/facilitator/internal/settlement"
	"github.com/ETH402/facilitator/internal/signer"
	"github.com/ETH402/facilitator/internal/stats"
	"github.com/ETH402/facilitator/internal/store"
	"github.com/ETH402/facilitator/internal/verification"
	exactfacilitator "github.com/x402-foundation/x402/go/v2/mechanisms/evm/exact/facilitator"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	logger.Info("starting ETH402", "config", cfg.RedactedSummary())

	root, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	database, err := store.Open(root, cfg.DatabaseURL, cfg.DatabaseMaxConns)
	if err != nil {
		logger.Error("database initialization failed", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	rpc := ethereum.NewClient(cfg.EthereumRPCURL, cfg.FallbackRPCURL, cfg.RPCTimeout, cfg.RPCReadRetries)
	verificationRPC, err := ethereum.NewVerificationClient(cfg.EthereumRPCURL, cfg.FallbackRPCURL)
	if err != nil {
		logger.Error("verification RPC initialization failed", "error", err)
		os.Exit(1)
	}
	defer verificationRPC.Close()
	verificationService := verification.New(
		exactfacilitator.NewExactEvmScheme(verificationRPC, nil),
		verificationRPC,
		database,
		cfg.RPCTimeout,
	)
	registry := metrics.New()
	statsService := stats.NewService(database, time.Now(), cfg.StatsCacheTTL)
	var sender email.Sender = email.LogSender{Logger: logger}
	if cfg.EmailBackend == "file" {
		sender = email.FileSender{Directory: cfg.EmailFileDir}
	}
	merchantService := merchant.New(database.Pool, sender, merchant.Config{
		BaseURL: cfg.PublicBaseURL, TermsVersion: cfg.TermsVersion,
		EmailTTL: cfg.EmailTokenTTL, Resend: cfg.EmailResend,
		WalletTTL: cfg.WalletChallengeTTL, RecipientCooldown: cfg.RecipientCooldown,
		Pepper: []byte(cfg.APIKeyPepper), BlockDisposable: cfg.BlockDisposable,
		RestrictFree: cfg.RestrictFreeEmail, Allowlist: cfg.EmailAllowlist, Denylist: cfg.EmailDenylist,
	})

	// Settlement is wired only when a signer is enabled; with the signer
	// disabled /settle reports settlement_unavailable and no workers run.
	var settlementService *settlement.Service
	if cfg.SignerMode != "disabled" {
		var transactionSigner signer.Signer
		switch cfg.SignerMode {
		case "development":
			development, err := signer.NewDevelopment(cfg.DevSignerKey)
			if err != nil {
				logger.Error("development signer initialization failed", "error", err)
				os.Exit(1)
			}
			transactionSigner = development
		case "external":
			kmsClient, err := signer.NewCloudKMSClient(root)
			if err != nil {
				logger.Error("Cloud KMS client initialization failed", "error", err)
				os.Exit(1)
			}
			defer func() { _ = kmsClient.Close() }()
			cloudKMS, err := signer.NewCloudKMS(root, kmsClient, cfg.KMSKeyName)
			if err != nil {
				logger.Error("Cloud KMS signer initialization failed", "error", err)
				os.Exit(1)
			}
			transactionSigner = cloudKMS
		default:
			// Config validation rejects every other mode; this is defense in
			// depth so a future mode cannot start without its backend.
			logger.Error("no signer backend implements the configured mode", "mode", cfg.SignerMode)
			os.Exit(1)
		}
		signerAddress, err := transactionSigner.Address(root)
		if err != nil {
			logger.Error("signer address resolution failed", "error", err)
			os.Exit(1)
		}
		chainNonce, err := rpc.TransactionCount(root, signerAddress)
		if err != nil {
			logger.Error("signer nonce seeding failed", "error", err)
			os.Exit(1)
		}
		seeded, err := store.SeedSignerAccount(root, database.Pool, signerAddress, chainNonce)
		if err != nil {
			logger.Error("signer account seeding failed", "error", err)
			os.Exit(1)
		}
		logger.Info("settlement signer enabled", "address", signerAddress, "next_nonce", seeded)
		settlementService = settlement.NewService(database, transactionSigner, rpc, settlement.Config{
			SignerAddress: signerAddress, ExpiryMargin: cfg.SettlementExpiryMargin,
			MerchantQuota: cfg.MerchantSettlementQuota, QuotaWindow: cfg.MerchantQuotaWindow,
			SigningTimeout: cfg.SigningTimeout, LeaseDuration: cfg.SettlementLeaseDuration,
			WorkerInterval: cfg.WorkerInterval, Confirmations: cfg.Confirmations,
			GasLimit: cfg.MaxGasLimit, MaxFeePerGas: cfg.MaxFeePerGasWei,
			MaxPriorityFeeGas: cfg.MaxPriorityFeeWei,
			RecoveryGrace:     cfg.SettlementRecoveryGrace, ReplacementAfter: cfg.SettlementReplacementAfter,
		}, logger)
		go settlementService.BroadcastWorker().Run(root)
		go settlementService.ConfirmationWorker().Run(root)
		go settlementService.RecoveryWorker().Run(root)
		// The hot balance is the bound on a signer compromise (ADR-0004 decision
		// 8), so publish it for alerting; a bound nobody watches is a convention.
		go settlementService.BalanceWorker(rpc, registry).Run(root)
	}
	api := httpapi.New(httpapi.Dependencies{
		Logger: logger, Database: database, Ethereum: rpc, Stats: statsService,
		Metrics: registry, ExpectedChainID: cfg.ChainID, PublicRatePerMinute: cfg.PublicRatePerMin,
		RegistrationRate: cfg.RegistrationRate, Merchant: merchantService,
		AllowedOrigin: cfg.PublicBaseURL, OperatorToken: cfg.OperatorToken,
		Verification: verificationService, Settlement: settlementService, MetricsEnabled: cfg.MetricsEnabled,
		TrustedProxies: cfg.TrustedProxies,
	})
	server := &http.Server{
		Addr: cfg.HTTPAddr, Handler: api.Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 32 << 10,
	}
	go func() {
		logger.Info("HTTP server listening", "address", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
			stop()
		}
	}()
	<-root.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
	logger.Info("ETH402 stopped")
}
