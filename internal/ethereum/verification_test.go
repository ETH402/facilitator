package ethereum

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	x402evm "github.com/x402-foundation/x402/go/v2/mechanisms/evm"
)

func verificationRPCServer(t *testing.T, results map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		result, ok := results[request.Method]
		if !ok {
			t.Errorf("unexpected method %q", request.Method)
			http.Error(w, "unexpected method", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":` + result + `}`))
	}))
}

func TestVerificationProvidersMustAgree(t *testing.T) {
	t.Parallel()
	primary := verificationRPCServer(t, map[string]string{"eth_chainId": `"0x1"`})
	defer primary.Close()
	fallback := verificationRPCServer(t, map[string]string{"eth_chainId": `"0x1"`})
	defer fallback.Close()

	client, err := NewVerificationClient(primary.URL, fallback.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer client.Close()
	chainID, err := client.GetChainID(context.Background())
	if err != nil || chainID.Uint64() != 1 {
		t.Fatalf("chain ID = %v, %v", chainID, err)
	}
}

func TestVerificationProviderDisagreementFailsClosed(t *testing.T) {
	t.Parallel()
	primary := verificationRPCServer(t, map[string]string{"eth_chainId": `"0x1"`})
	defer primary.Close()
	fallback := verificationRPCServer(t, map[string]string{"eth_chainId": `"0x2"`})
	defer fallback.Close()

	client, err := NewVerificationClient(primary.URL, fallback.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer client.Close()
	observer := &countingObserver{}
	client.ObserveProviderDisagreements(observer)
	if _, err = client.GetChainID(context.Background()); !errors.Is(err, ErrProviderDisagreement) {
		t.Fatalf("error = %v, want provider disagreement", err)
	}
	if observer.disagreements.Load() != 1 {
		t.Fatalf("disagreements = %d, want 1", observer.disagreements.Load())
	}
}

func TestVerificationCodeDisagreementFailsClosed(t *testing.T) {
	t.Parallel()
	primary := verificationRPCServer(t, map[string]string{"eth_getCode": `"0x"`})
	defer primary.Close()
	fallback := verificationRPCServer(t, map[string]string{"eth_getCode": `"0x6000"`})
	defer fallback.Close()

	client, err := NewVerificationClient(primary.URL, fallback.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer client.Close()
	if _, err = client.GetCode(context.Background(), "0x1111111111111111111111111111111111111111"); !errors.Is(err, ErrProviderDisagreement) {
		t.Fatalf("error = %v, want provider disagreement", err)
	}
}

func TestVerificationContractReadDisagreementFailsClosed(t *testing.T) {
	t.Parallel()
	primary := verificationRPCServer(t, map[string]string{"eth_call": `"0x` + strings.Repeat("0", 64) + `"`})
	defer primary.Close()
	fallback := verificationRPCServer(t, map[string]string{"eth_call": `"0x` + strings.Repeat("0", 63) + `1"`})
	defer fallback.Close()

	client, err := NewVerificationClient(primary.URL, fallback.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer client.Close()
	var nonce [32]byte
	_, err = client.ReadContract(context.Background(),
		"0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
		x402evm.AuthorizationStateABI,
		x402evm.FunctionAuthorizationState,
		common.HexToAddress("0x1111111111111111111111111111111111111111"), nonce)
	if !errors.Is(err, ErrProviderDisagreement) {
		t.Fatalf("error = %v, want provider disagreement", err)
	}
}

func TestVerificationProviderFailureDoesNotDegradeToOneProvider(t *testing.T) {
	t.Parallel()
	primary := verificationRPCServer(t, map[string]string{"eth_chainId": `"0x1"`})
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer fallback.Close()

	client, err := NewVerificationClient(primary.URL, fallback.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer client.Close()
	observer := &countingObserver{}
	client.ObserveProviderDisagreements(observer)
	if _, err = client.GetChainID(context.Background()); !errors.Is(err, ErrProviderDisagreement) {
		t.Fatalf("error = %v, want error-vs-value disagreement", err)
	}
	if observer.disagreements.Load() != 1 {
		t.Fatalf("disagreements = %d, want 1", observer.disagreements.Load())
	}
}

func TestVerificationWaitsForAllOutcomesBeforeClassifyingDivergence(t *testing.T) {
	first, second := new(ethclient.Client), new(ethclient.Client)
	errorReturned := make(chan struct{})
	releaseSuccess := make(chan struct{})
	observer := &countingObserver{}
	type result struct {
		value string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := readAgreement(context.Background(), []*ethclient.Client{first, second},
			func(client *ethclient.Client) (string, error) {
				if client == second {
					close(errorReturned)
					return "", errors.New("provider unavailable")
				}
				<-releaseSuccess
				return "0x1", nil
			}, func(left, right string) bool { return left == right }, observer)
		done <- result{value: value, err: err}
	}()
	<-errorReturned
	select {
	case outcome := <-done:
		t.Fatalf("read returned before collecting successful provider outcome: %+v", outcome)
	default:
	}
	close(releaseSuccess)
	outcome := <-done
	if !errors.Is(outcome.err, ErrProviderDisagreement) || outcome.value != "" {
		t.Fatalf("outcome = %+v, want fail-closed disagreement", outcome)
	}
	if observer.disagreements.Load() != 1 {
		t.Fatalf("disagreements = %d, want 1", observer.disagreements.Load())
	}
}

func TestVerificationAllProvidersFailAsAvailabilityFailure(t *testing.T) {
	t.Parallel()
	server := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}))
	}
	primary, fallback := server(), server()
	defer primary.Close()
	defer fallback.Close()
	client, err := NewVerificationClient(primary.URL, fallback.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer client.Close()
	observer := &countingObserver{}
	client.ObserveProviderDisagreements(observer)
	if _, err = client.GetChainID(context.Background()); err == nil || errors.Is(err, ErrProviderDisagreement) {
		t.Fatalf("error = %v, want provider availability failure", err)
	}
	if observer.disagreements.Load() != 0 {
		t.Fatalf("all-provider outage recorded %d disagreements", observer.disagreements.Load())
	}
}

func TestVerificationTransportErrorsDoNotExposeEndpointCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := server.URL + "/private-project-token?api-key=query-credential"
	server.Close()
	client, err := NewVerificationClient(endpoint, "")
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer client.Close()
	_, err = client.GetChainID(context.Background())
	if err == nil {
		t.Fatal("closed authenticated endpoint unexpectedly answered")
	}
	for _, secret := range []string{"private-project-token", "query-credential", "api-key"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("verification transport error exposed endpoint credential %q: %v", secret, err)
		}
	}
}
