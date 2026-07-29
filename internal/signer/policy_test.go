package signer

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ETH402/facilitator/internal/config"
	"github.com/ETH402/facilitator/internal/policy"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	boundaryToken = "policy-signer-token-for-tests-0123456789"
	boundaryKey   = "0x" + "00000000000000000000000000000000000000000000000000000000000000" + "01"
	otherKey      = "0x" + "00000000000000000000000000000000000000000000000000000000000000" + "02"
)

func policyLimits() policy.Limits {
	return policy.Limits{
		MaxFeePerGasWei:         big.NewInt(80_000_000_000),
		MaxPriorityFeePerGasWei: big.NewInt(2_000_000_000),
		MaxGasLimit:             250_000,
	}
}

func policyAuthorization() policy.Authorization {
	return policy.Authorization{
		From:        "0x1111111111111111111111111111111111111111",
		To:          "0x2222222222222222222222222222222222222222",
		Value:       "1500000",
		ValidAfter:  "1700000000",
		ValidBefore: "1700003600",
		Nonce:       "0x" + strings.Repeat("ab", 32),
		Signature:   "0x" + strings.Repeat("cd", 64) + "1b",
	}
}

// fakeBoundary is a faithful stand-in for cmd/policysigner: it builds the
// transaction from the authorization exactly as the real boundary does. tamper
// lets a test make it hostile, which is the only way to exercise the client's
// verification of a service it must not blindly trust.
//
// It signs the transaction directly with an ECDSA key rather than through
// signer.Transaction. Going through that type would silently discard some
// tampering — its Value field is a string the caller sets, so a test raising the
// ether value would produce an untampered transaction and pass for the wrong
// reason. A hostile service is under no such constraint, so neither is this fake.
type fakeBoundary struct {
	backend  Signer
	identity string
	tamper   func(*types.DynamicFeeTx)
	key      *ecdsa.PrivateKey
}

func newFakeBoundary(t *testing.T) *fakeBoundary {
	t.Helper()
	backend, err := NewDevelopment(boundaryKey)
	if err != nil {
		t.Fatalf("development signer: %v", err)
	}
	address, err := backend.Address(context.Background())
	if err != nil {
		t.Fatalf("signer address: %v", err)
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(boundaryKey, "0x"))
	if err != nil {
		t.Fatalf("parse boundary key: %v", err)
	}
	return &fakeBoundary{backend: backend, identity: address, key: key}
}

func (b *fakeBoundary) serve(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /identity", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.Header.Get("Authorization"), boundaryToken) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(policy.Response{SignerAddress: b.identity})
	})
	mux.HandleFunc("POST /sign", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.Header.Get("Authorization"), boundaryToken) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var request policy.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		unsigned, err := policy.Unsigned(request, policyLimits())
		if err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		inner := &types.DynamicFeeTx{
			ChainID: unsigned.ChainId(), Nonce: unsigned.Nonce(), To: unsigned.To(),
			Value: big.NewInt(0), Gas: unsigned.Gas(),
			GasFeeCap: unsigned.GasFeeCap(), GasTipCap: unsigned.GasTipCap(),
			Data: unsigned.Data(),
		}
		if b.tamper != nil {
			b.tamper(inner)
		}
		signed, err := types.SignTx(types.NewTx(inner),
			types.LatestSignerForChainID(inner.ChainID), b.key)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		raw, err := signed.MarshalBinary()
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(policy.Response{
			RawTransaction: "0x" + hex.EncodeToString(raw),
			SignerAddress:  b.identity,
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func policyTransaction(t *testing.T) Transaction {
	t.Helper()
	auth := policyAuthorization()
	calldata, err := policy.TransferWithAuthorizationData(auth)
	if err != nil {
		t.Fatalf("build calldata: %v", err)
	}
	return Transaction{
		ChainID:              config.MainnetChainID,
		Nonce:                7,
		To:                   config.MainnetUSDC,
		Data:                 calldata,
		Value:                "0",
		GasLimit:             120_000,
		MaxFeePerGas:         "40000000000",
		MaxPriorityFeePerGas: "1000000000",
		Authorization:        &auth,
	}
}

func TestPolicyClientSignsThroughTheBoundary(t *testing.T) {
	boundary := newFakeBoundary(t)
	server := boundary.serve(t)
	client, err := NewPolicyClient(context.Background(), server.URL, boundaryToken, 5*time.Second)
	if err != nil {
		t.Fatalf("NewPolicyClient: %v", err)
	}
	if got, _ := client.Address(context.Background()); got != boundary.identity {
		t.Errorf("address = %s, want %s", got, boundary.identity)
	}

	want := policyTransaction(t)
	signed, err := client.SignTransaction(context.Background(), want)
	if err != nil {
		t.Fatalf("SignTransaction: %v", err)
	}

	// The sighash must be the one this process derives, and it must match what a
	// direct backend would produce for the same transaction. Settlement recovery
	// re-signs against this value to prove a transaction's identity, so a
	// boundary-supplied one would move that proof outside this process.
	direct, err := boundary.backend.SignTransaction(context.Background(), want)
	if err != nil {
		t.Fatalf("direct sign: %v", err)
	}
	if signed.SigHash != direct.SigHash {
		t.Errorf("sighash = %x, want %x", signed.SigHash, direct.SigHash)
	}

	var decoded types.Transaction
	if err := decoded.UnmarshalBinary(signed.Raw); err != nil {
		t.Fatalf("decode returned transaction: %v", err)
	}
	if decoded.Nonce() != want.Nonce {
		t.Errorf("nonce = %d, want %d", decoded.Nonce(), want.Nonce)
	}
	sender, err := types.Sender(types.LatestSignerForChainID(big.NewInt(1)), &decoded)
	if err != nil {
		t.Fatalf("recover sender: %v", err)
	}
	if !strings.EqualFold(sender.Hex(), boundary.identity) {
		t.Errorf("sender = %s, want %s", sender.Hex(), boundary.identity)
	}
}

// TestPolicyClientRejectsASubstitutedTransaction is the other half of the trust
// model. Settlement records the returned transaction's hash as the identity of a
// real payment and re-signs against its sighash during recovery, so a boundary
// that answered with a *different* transaction would have that one recorded as
// though it were the intended one. Every field that determines behaviour is
// therefore checked against what was asked for.
func TestPolicyClientRejectsASubstitutedTransaction(t *testing.T) {
	tampering := map[string]func(*types.DynamicFeeTx){
		"different nonce": func(tx *types.DynamicFeeTx) { tx.Nonce++ },
		"different recipient": func(tx *types.DynamicFeeTx) {
			other := common.HexToAddress("0x4444444444444444444444444444444444444444")
			tx.To = &other
		},
		"non-zero ether value": func(tx *types.DynamicFeeTx) { tx.Value = big.NewInt(1) },
		"different calldata": func(tx *types.DynamicFeeTx) {
			altered := make([]byte, len(tx.Data))
			copy(altered, tx.Data)
			altered[len(altered)-1] ^= 0xff
			tx.Data = altered
		},
		"raised gas limit":    func(tx *types.DynamicFeeTx) { tx.Gas += 10_000 },
		"raised max fee":      func(tx *types.DynamicFeeTx) { tx.GasFeeCap = big.NewInt(79_000_000_000) },
		"raised priority fee": func(tx *types.DynamicFeeTx) { tx.GasTipCap = big.NewInt(1_900_000_000) },
		"different chain":     func(tx *types.DynamicFeeTx) { tx.ChainID = big.NewInt(8453) },
	}
	for name, tamper := range tampering {
		t.Run(name, func(t *testing.T) {
			boundary := newFakeBoundary(t)
			server := boundary.serve(t)
			client, err := NewPolicyClient(context.Background(), server.URL, boundaryToken, 5*time.Second)
			if err != nil {
				t.Fatalf("NewPolicyClient: %v", err)
			}
			boundary.tamper = tamper
			if _, err := client.SignTransaction(context.Background(), policyTransaction(t)); err == nil {
				t.Fatal("accepted a transaction that was not the one requested")
			}
		})
	}
}

// TestPolicyClientRejectsAnUnexpectedSigner catches a valid signature from the
// wrong key. The nonce sequence in signer_accounts belongs to one address, so
// accepting this would strand every transaction queued behind it.
func TestPolicyClientRejectsAnUnexpectedSigner(t *testing.T) {
	boundary := newFakeBoundary(t)
	server := boundary.serve(t)
	client, err := NewPolicyClient(context.Background(), server.URL, boundaryToken, 5*time.Second)
	if err != nil {
		t.Fatalf("NewPolicyClient: %v", err)
	}
	impostor, err := crypto.HexToECDSA(strings.TrimPrefix(otherKey, "0x"))
	if err != nil {
		t.Fatalf("parse impostor key: %v", err)
	}
	boundary.key = impostor
	_, err = client.SignTransaction(context.Background(), policyTransaction(t))
	if err == nil || !strings.Contains(err.Error(), "signed by") {
		t.Fatalf("error = %v, want a wrong-signer rejection", err)
	}
}

func TestPolicyClientRequiresTheAuthorization(t *testing.T) {
	boundary := newFakeBoundary(t)
	server := boundary.serve(t)
	client, err := NewPolicyClient(context.Background(), server.URL, boundaryToken, 5*time.Second)
	if err != nil {
		t.Fatalf("NewPolicyClient: %v", err)
	}
	tx := policyTransaction(t)
	tx.Authorization = nil
	if _, err := client.SignTransaction(context.Background(), tx); err == nil {
		t.Fatal("signed without the authorization the boundary builds calldata from")
	}
}

func TestPolicyClientFailsFastOnABadToken(t *testing.T) {
	boundary := newFakeBoundary(t)
	server := boundary.serve(t)
	// Startup, not the first payment: a wrong token is a deployment error and
	// should stop the process rather than fail every settlement silently later.
	if _, err := NewPolicyClient(context.Background(), server.URL, strings.Repeat("w", 40), 5*time.Second); err == nil {
		t.Fatal("constructed a client with a token the boundary rejects")
	}
}

func TestPolicyClientRejectsInvalidBoundaryIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(policy.Response{SignerAddress: "not-an-address"})
	}))
	defer server.Close()
	if _, err := NewPolicyClient(context.Background(), server.URL, boundaryToken, time.Second); err == nil {
		t.Fatal("constructed client with an invalid signer identity")
	}
}

func TestPolicyClientRequiresAnOriginEndpoint(t *testing.T) {
	for _, endpoint := range []string{
		"https://user:password@signer.example",
		"https://signer.example/path",
		"https://signer.example?destination=elsewhere",
		"file:///tmp/signer",
	} {
		if _, err := NewPolicyClient(context.Background(), endpoint, boundaryToken, time.Second); err == nil {
			t.Errorf("accepted non-origin endpoint %q", endpoint)
		}
	}
}

func TestPolicyClientRefusesRedirects(t *testing.T) {
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected = true
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	if _, err := NewPolicyClient(context.Background(), redirector.URL, boundaryToken, time.Second); err == nil {
		t.Fatal("constructed client through a redirect")
	}
	if redirected {
		t.Fatal("policy signer redirect was followed")
	}
}

func TestPolicyClientDoesNotPropagateBoundaryBody(t *testing.T) {
	const sensitive = "attacker-controlled-sensitive-response"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/identity":
			_ = json.NewEncoder(w).Encode(policy.Response{
				SignerAddress: common.HexToAddress("0x1234").Hex(),
			})
		case "/sign":
			http.Error(w, sensitive, http.StatusBadGateway)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewPolicyClient(context.Background(), server.URL, boundaryToken, time.Second)
	if err != nil {
		t.Fatalf("NewPolicyClient: %v", err)
	}
	_, err = client.SignTransaction(context.Background(), policyTransaction(t))
	if err == nil {
		t.Fatal("boundary refusal was not surfaced")
	}
	if strings.Contains(err.Error(), sensitive) {
		t.Fatal("boundary-controlled response body propagated into error")
	}
}

func TestPolicyClientPropagatesABoundaryRefusal(t *testing.T) {
	boundary := newFakeBoundary(t)
	server := boundary.serve(t)
	client, err := NewPolicyClient(context.Background(), server.URL, boundaryToken, 5*time.Second)
	if err != nil {
		t.Fatalf("NewPolicyClient: %v", err)
	}
	// Over the boundary's ceiling but under this process's own configuration, so
	// only the boundary can refuse it. The refusal must surface as an error rather
	// than an empty signature that settlement might record.
	tx := policyTransaction(t)
	tx.GasLimit = 240_000
	tx.MaxFeePerGas = "90000000000"
	tx.MaxPriorityFeePerGas = "1000000000"
	if _, err := client.SignTransaction(context.Background(), tx); err == nil {
		t.Fatal("a boundary refusal was not surfaced as an error")
	}
}
