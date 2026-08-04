package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ETH402/facilitator/internal/config"
	"github.com/ETH402/facilitator/internal/email"
	"github.com/ETH402/facilitator/internal/ethereum"
	"github.com/ETH402/facilitator/internal/httpapi"
	"github.com/ETH402/facilitator/internal/merchant"
	"github.com/ETH402/facilitator/internal/metrics"
	"github.com/ETH402/facilitator/internal/migrate"
	"github.com/ETH402/facilitator/internal/retention"
	"github.com/ETH402/facilitator/internal/settlement"
	"github.com/ETH402/facilitator/internal/signer"
	"github.com/ETH402/facilitator/internal/stats"
	"github.com/ETH402/facilitator/internal/store"
	"github.com/ETH402/facilitator/internal/verification"
	"github.com/ETH402/facilitator/migrations"
	exactfacilitator "github.com/x402-foundation/x402/go/v2/mechanisms/evm/exact/facilitator"
)

// healthCheck probes the local readiness endpoint and exits.
//
// The runtime image is distroless: no shell, no curl, nothing a container
// healthcheck could otherwise invoke. Letting the binary probe itself keeps the
// image minimal without leaving the container unmonitored. /health/ready checks
// PostgreSQL and that the RPC reports chain 1, so this fails closed when a
// dependency is down.
func healthCheck(addr string) int {
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 4 * time.Second}
	response, err := client.Get("http://" + addr + "/health/ready")
	if err != nil {
		fmt.Fprintf(os.Stderr, "health check: %v\n", err)
		return 1
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "health check: status %d\n", response.StatusCode)
		return 1
	}
	return 0
}

// workerDrainTimeout bounds how long shutdown waits for a settlement worker tick
// to finish. Beyond it, exiting is preferable to hanging — and whatever was in
// flight is resolvable by the recovery worker on the next start.
const workerDrainTimeout = 45 * time.Second

func main() {
	probe := flag.Bool("health-check", false,
		"probe the local readiness endpoint and exit; used by the container healthcheck")
	checkConfig := flag.Bool("check-config", false,
		"validate configuration without connecting to external dependencies")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.SlogLevel(),
	}))
	if *checkConfig {
		logger.Info("configuration valid", "config", cfg.RedactedSummary())
		return
	}
	if *probe {
		// Runs after full configuration validation, so a container with broken
		// configuration reports unhealthy rather than probing a port that will
		// never open. Only HTTPAddr is used; the dependencies themselves are
		// checked by the readiness endpoint being probed.
		os.Exit(healthCheck(cfg.HTTPAddr))
	}
	logger.Info("starting ETH402", "config", cfg.RedactedSummary())

	root, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var workers sync.WaitGroup
	database, err := store.Open(root, cfg.DatabaseURL, cfg.DatabaseMaxConns)
	if err != nil {
		logger.Error("database initialization failed", "error", err)
		os.Exit(1)
	}
	defer database.Close()
	if err := migrate.CheckApplied(root, database.Pool, migrations.Files); err != nil {
		logger.Error("database schema validation failed", "error", err)
		os.Exit(1)
	}

	rpc := ethereum.NewClient(cfg.EthereumRPCURL, cfg.FallbackRPCURL, cfg.RPCTimeout, cfg.RPCReadRetries)
	if err := ethereum.ValidateProviders(root, cfg.EthereumRPCURL, cfg.FallbackRPCURL,
		cfg.RPCTimeout, cfg.ChainID); err != nil {
		logger.Error("Ethereum RPC validation failed", "error", err)
		os.Exit(1)
	}
	registry := metrics.New()
	rpc.Observe(registry)
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
	// The status page derives settlement health from the same worker heartbeats
	// Prometheus scrapes, rather than a second notion of health that could disagree
	// with the alerts. Probes run on the cache's schedule, not per request, because
	// /stats and /status are public and unauthenticated.
	statsService := stats.NewService(stats.Config{
		Source:  database,
		Started: time.Now(),
		TTL:     cfg.StatsCacheTTL,
		Health: stats.NewAssessor(stats.AssessorConfig{
			Database:          database,
			Chain:             rpc,
			ExpectedChainID:   cfg.ChainID,
			Heartbeats:        registry,
			ExpectedWorkers:   []string{"broadcast", "confirmation", "recovery"},
			StaleAfter:        cfg.StaleAfter(),
			SettlementEnabled: cfg.SignerMode != "disabled",
			ProbeTimeout:      cfg.RPCTimeout,
		}),
		PublishVolume: cfg.PublishStatsVolume,
	})
	var sender email.Sender = email.LogSender{Logger: logger}
	switch cfg.EmailBackend {
	case "file":
		sender = email.FileSender{Directory: cfg.EmailFileDir}
	case "smtp":
		smtpSender, err := email.NewSMTPSender(email.SMTPConfig{
			Address: cfg.SMTPAddress, Username: cfg.SMTPUsername,
			Password: cfg.SMTPPassword, From: cfg.SMTPFrom,
			TLSMode: cfg.SMTPTLSMode, Timeout: cfg.SMTPTimeout,
		})
		if err != nil {
			logger.Error("SMTP sender initialization failed", "error", err)
			os.Exit(1)
		}
		if cfg.Environment == "production" {
			probeContext, cancel := context.WithTimeout(root, cfg.SMTPTimeout)
			if err := smtpSender.Probe(probeContext); err != nil {
				cancel()
				logger.Error("SMTP production preflight failed", "error", err)
				os.Exit(1)
			}
			cancel()
		}
		sender = smtpSender
	}
	merchantService := merchant.New(database.Pool, sender, merchant.Config{
		BaseURL: cfg.PublicBaseURL, TermsVersion: cfg.TermsVersion,
		EmailTTL: cfg.EmailTokenTTL, Resend: cfg.EmailResend,
		WalletTTL: cfg.WalletChallengeTTL, RecipientCooldown: cfg.RecipientCooldown,
		AdminSessionTTL: cfg.MerchantSessionTTL, PaymentRetention: cfg.PaymentRetention,
		PublicDirectoryTTL: cfg.StatsCacheTTL,
		Pepper:             []byte(cfg.APIKeyPepper), BlockDisposable: cfg.BlockDisposable,
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
		case "policy":
			// The boundary holds the KMS grant; this process holds only a bearer
			// token. See internal/policy and cmd/policysigner.
			policySigner, err := signer.NewPolicyClient(root, cfg.PolicySignerURL,
				cfg.PolicySignerToken, cfg.SigningTimeout)
			if err != nil {
				logger.Error("policy signer initialization failed", "error", err)
				os.Exit(1)
			}
			transactionSigner = policySigner
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
			MerchantQuota: cfg.MerchantSettlementQuota, GlobalQuota: cfg.GlobalSettlementQuota, QuotaWindow: cfg.MerchantQuotaWindow,
			SigningTimeout: cfg.SigningTimeout, LeaseDuration: cfg.SettlementLeaseDuration,
			WorkerInterval: cfg.WorkerInterval, Confirmations: cfg.Confirmations,
			GasLimit: cfg.MaxGasLimit, MaxFeePerGas: cfg.MaxFeePerGasWei,
			MaxPriorityFeeGas: cfg.MaxPriorityFeeWei,
			RecoveryGrace:     cfg.SettlementRecoveryGrace, ReplacementAfter: cfg.SettlementReplacementAfter,
		}, logger)
		settlementService.Observe(registry)
		// Tracked so shutdown can wait for an in-flight tick. A broadcast
		// interrupted between sending and recording its hash becomes an ambiguous
		// transaction, and resolving one costs a human — so a deploy must not be
		// able to create them.
		start := func(run func(context.Context)) {
			workers.Add(1)
			go func() {
				defer workers.Done()
				run(root)
			}()
		}
		start(settlementService.BroadcastWorker().Run)
		start(settlementService.ConfirmationWorker().Run)
		start(settlementService.RecoveryWorker().Run)
		// The hot balance is the bound on a signer compromise (ADR-0004 decision
		// 8), so publish it for alerting; a bound nobody watches is a convention.
		start(settlementService.BalanceWorker(rpc, registry).Run)
	}
	// Fair-use pruning runs whether or not settlement is enabled: the counters are
	// written by authenticated merchant endpoints, which exist either way. Retained
	// rows would turn a rate counter into an indefinite per-merchant activity log,
	// which docs/PRIVACY.md would then have to account for.
	if cfg.MerchantRequestsPerWindow > 0 && cfg.FairUseWindow > 0 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			pruneFairUse(root, database, cfg.FairUseWindow, logger)
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		retention.New(retention.Config{
			Store: database, PaymentAfter: cfg.PaymentRetention,
			EphemeralAfter:  cfg.EphemeralRetention,
			RevokedKeyAfter: cfg.RevokedKeyRetention,
			Interval:        cfg.RetentionInterval, BatchSize: cfg.RetentionBatchSize,
			Logger: logger, Observer: registry,
		}).Run(root)
	}()
	api := httpapi.New(httpapi.Dependencies{
		Logger: logger, Database: database, Ethereum: rpc, Stats: statsService,
		Metrics: registry, ExpectedChainID: cfg.ChainID, PublicRatePerMinute: cfg.PublicRatePerMin,
		FairUse: database, MerchantRequestsPerWindow: cfg.MerchantRequestsPerWindow,
		FairUseWindow:    cfg.FairUseWindow,
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
	// Then wait for the settlement workers. Their in-flight broadcasts are
	// detached from this cancellation precisely so they can finish recording what
	// they sent; exiting underneath one would undo that.
	drained := make(chan struct{})
	go func() {
		workers.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(workerDrainTimeout):
		logger.Warn("settlement workers did not drain in time; in-flight work may need recovery",
			"timeout", workerDrainTimeout)
	}
	logger.Info("ETH402 stopped")
}

// pruneFairUse deletes elapsed fair-use windows on a slow cadence. It is not
// latency-sensitive and the rows are tiny, so it runs no more often than the window
// itself; the delete is bounded by an index on window_start.
//
// Unlike the settlement workers this needs no shutdown protection: a prune
// interrupted halfway just leaves rows for the next tick, whereas an interrupted
// broadcast strands a payment.
func pruneFairUse(ctx context.Context, database *store.Store, window time.Duration, logger *slog.Logger) {
	interval := max(window, time.Hour)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Recover per tick, like the settlement workers' guard: this
			// goroutine is bare, so an unrecovered panic would terminate the
			// whole process and take HTTP serving down with it.
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						logger.ErrorContext(ctx, "fair-use pruning panic recovered",
							"panic", recovered, "stack", string(debug.Stack()))
					}
				}()
				removed, err := store.PruneMerchantUsage(ctx, database.Pool, window, time.Now())
				if err != nil {
					if !errors.Is(err, context.Canceled) {
						logger.WarnContext(ctx, "fair-use pruning failed", "error", err)
					}
					return
				}
				if removed > 0 {
					logger.InfoContext(ctx, "pruned elapsed fair-use windows", "rows", removed)
				}
			}()
		}
	}
}
