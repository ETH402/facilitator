// Command policysigner is the signing boundary described in internal/policy.
//
// It runs as a separate process with its own identity, and it is the only thing
// holding permission to use the Cloud KMS key. The facilitator asks it for
// signatures over authorization *fields*; it builds the transaction itself, so a
// compromise of the facilitator cannot produce an ether transfer, a call to
// another contract, or a call to another function — those are not expressible in
// the request.
//
// Deploy it with a service identity distinct from the facilitator's, and grant
// roles/cloudkms.signerVerifier on the one key version to this identity only.
// Leaving that grant on the facilitator as well defeats the entire arrangement.
package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ETH402/facilitator/internal/policy"
	"github.com/ETH402/facilitator/internal/signer"
	"github.com/ethereum/go-ethereum/core/types"
)

const (
	readHeaderTimeout = 5 * time.Second
	requestTimeout    = 20 * time.Second
	shutdownTimeout   = 10 * time.Second
	maxRequestBytes   = 8 << 10
)

func main() {
	if err := run(); err != nil {
		slog.Error("policy signer stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	addr := env("POLICYSIGNER_LISTEN_ADDRESS", "127.0.0.1:8081")
	token := os.Getenv("POLICYSIGNER_TOKEN")
	keyName := os.Getenv("POLICYSIGNER_KMS_KEY_NAME")
	limits, err := limitsFromEnv()
	if err != nil {
		return err
	}
	if len(token) < 32 {
		// The token is the only thing separating this from an open signing
		// service, so a weak one is a configuration error rather than a warning.
		return errors.New("POLICYSIGNER_TOKEN must be set to at least 32 characters")
	}
	if keyName == "" {
		return errors.New("POLICYSIGNER_KMS_KEY_NAME must name a Cloud KMS key version")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	kmsClient, err := signer.NewCloudKMSClient(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = kmsClient.Close() }()
	kms, err := signer.NewCloudKMS(ctx, kmsClient, keyName)
	if err != nil {
		return err
	}
	address, err := kms.Address(ctx)
	if err != nil {
		return err
	}

	boundary := &boundary{signer: kms, address: address, limits: limits, token: token, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sign", boundary.sign)
	mux.HandleFunc("GET /identity", boundary.identity)
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           http.TimeoutHandler(mux, requestTimeout, `{"error":"timeout"}`),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	logger.Info("policy signer listening", "address", addr, "signer_address", address,
		"max_gas_limit", limits.MaxGasLimit, "max_fee_per_gas_wei", limits.MaxFeePerGasWei.String())

	errs := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

type boundary struct {
	signer  signer.Signer
	address string
	limits  policy.Limits
	token   string
	logger  *slog.Logger
}

// authorized compares in constant time. A signing service that leaks its token
// through response timing is a poor place to put an allowlist.
func (b *boundary) authorized(r *http.Request) bool {
	presented := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(presented), []byte(b.token)) == 1
}

func (b *boundary) identity(w http.ResponseWriter, r *http.Request) {
	if !b.authorized(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, policy.Response{SignerAddress: b.address})
}

func (b *boundary) sign(w http.ResponseWriter, r *http.Request) {
	if !b.authorized(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var request policy.Request
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		// Unknown fields are refused rather than ignored: a caller sending a
		// field this boundary does not understand may believe it constrains
		// something, and silently dropping it would make that belief false.
		writeError(w, http.StatusBadRequest, "malformed signing request")
		return
	}

	unsigned, err := policy.Unsigned(request, b.limits)
	if err != nil {
		// Logged in full, because a rejection here is either a bug in the caller
		// or the first observable sign that the caller is compromised. Returned
		// in full too: both processes are operated by the same team.
		b.logger.WarnContext(r.Context(), "refused to sign", "error", err,
			"nonce", request.Nonce, "gas_limit", request.GasLimit,
			"payer", request.Authorization.From, "payee", request.Authorization.To)
		status := http.StatusBadRequest
		if errors.Is(err, policy.ErrOverLimit) {
			status = http.StatusUnprocessableEntity
		}
		writeError(w, status, err.Error())
		return
	}

	// The boundary hands the backend the transaction it built, never the caller's
	// description of one. signer.Transaction carries the calldata this boundary
	// packed, so signer.Validate re-checks this boundary's own work.
	signed, err := b.signer.SignTransaction(r.Context(), signer.Transaction{
		ChainID:              unsigned.ChainId().Uint64(),
		Nonce:                unsigned.Nonce(),
		To:                   unsigned.To().Hex(),
		Data:                 unsigned.Data(),
		Value:                "0",
		GasLimit:             unsigned.Gas(),
		MaxFeePerGas:         unsigned.GasFeeCap().String(),
		MaxPriorityFeePerGas: unsigned.GasTipCap().String(),
	})
	if err != nil {
		b.logger.ErrorContext(r.Context(), "signing failed", "error", err, "nonce", request.Nonce)
		writeError(w, http.StatusBadGateway, "signing failed")
		return
	}
	// Confirming the backend signed the transaction this boundary built. Without
	// it the boundary would be an allowlist that never checks its own output.
	if err := confirmSigned(signed.Raw, unsigned); err != nil {
		b.logger.ErrorContext(r.Context(), "backend returned an unexpected transaction",
			"error", err, "nonce", request.Nonce)
		writeError(w, http.StatusBadGateway, "signing failed")
		return
	}
	b.logger.InfoContext(r.Context(), "signed settlement transaction",
		"nonce", request.Nonce, "payer", request.Authorization.From,
		"payee", request.Authorization.To, "value", request.Authorization.Value)
	writeJSON(w, http.StatusOK, policy.Response{
		RawTransaction: "0x" + hex.EncodeToString(signed.Raw),
		SignerAddress:  b.address,
	})
}

// confirmSigned requires the signed bytes to decode to the same transaction that
// was built, comparing the field that determines behaviour rather than the whole
// encoding, which necessarily differs by the signature.
func confirmSigned(raw []byte, want *types.Transaction) error {
	var got types.Transaction
	if err := got.UnmarshalBinary(raw); err != nil {
		return fmt.Errorf("decode signed transaction: %w", err)
	}
	if got.ChainId().Cmp(want.ChainId()) != 0 || got.Nonce() != want.Nonce() ||
		got.To() == nil || *got.To() != *want.To() || got.Value().Sign() != 0 ||
		got.Gas() != want.Gas() || got.GasFeeCap().Cmp(want.GasFeeCap()) != 0 ||
		got.GasTipCap().Cmp(want.GasTipCap()) != 0 ||
		!bytes.Equal(got.Data(), want.Data()) {
		return errors.New("signed transaction differs from the one built")
	}
	return nil
}

// limitsFromEnv reads the ceilings from this process's own configuration. They are
// never read from a request: a compromised caller would raise them.
func limitsFromEnv() (policy.Limits, error) {
	maxFee, ok := new(big.Int).SetString(env("POLICYSIGNER_MAX_FEE_PER_GAS_WEI", ""), 10)
	if !ok || maxFee.Sign() <= 0 {
		return policy.Limits{}, errors.New("POLICYSIGNER_MAX_FEE_PER_GAS_WEI must be a positive decimal wei value")
	}
	priorityFee, ok := new(big.Int).SetString(env("POLICYSIGNER_MAX_PRIORITY_FEE_PER_GAS_WEI", ""), 10)
	if !ok || priorityFee.Sign() < 0 {
		return policy.Limits{}, errors.New("POLICYSIGNER_MAX_PRIORITY_FEE_PER_GAS_WEI must be a decimal wei value")
	}
	gasLimit, err := strconv.ParseUint(env("POLICYSIGNER_MAX_GAS_LIMIT", ""), 10, 64)
	if err != nil || gasLimit == 0 {
		return policy.Limits{}, errors.New("POLICYSIGNER_MAX_GAS_LIMIT must be a positive integer")
	}
	return policy.Limits{
		MaxFeePerGasWei:         maxFee,
		MaxPriorityFeePerGasWei: priorityFee,
		MaxGasLimit:             gasLimit,
	}, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
