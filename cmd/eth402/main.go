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
	api := httpapi.New(httpapi.Dependencies{
		Logger: logger, Database: database, Ethereum: rpc, Stats: statsService,
		Metrics: registry, ExpectedChainID: cfg.ChainID, PublicRatePerMinute: cfg.PublicRatePerMin,
		RegistrationRate: cfg.RegistrationRate, Merchant: merchantService,
		AllowedOrigin: cfg.PublicBaseURL, OperatorToken: cfg.OperatorToken,
		Verification: verificationService,
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
