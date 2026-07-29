package signer

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ETH402/facilitator/internal/policy"
	"github.com/ethereum/go-ethereum/core/types"
)

// PolicyClient signs through the policy boundary rather than reaching Cloud KMS
// directly, so the calldata allowlist survives a compromise of this process.
//
// It sends the authorization fields and never calldata: the boundary builds what
// it signs. That is the whole point — see internal/policy.
//
// It then checks the boundary's answer rather than trusting it. A signing service
// is a tempting thing to compromise in the other direction, and settlement records
// the returned transaction's hash as the identity of a real payment, so a
// substituted transaction would be recorded as though it were the intended one.
type PolicyClient struct {
	endpoint string
	token    string
	http     *http.Client
	address  string
}

// NewPolicyClient resolves the boundary's signing identity at construction, which
// doubles as a reachability and authentication check: a misconfigured token fails
// at startup rather than on the first payment.
func NewPolicyClient(ctx context.Context, endpoint, token string, timeout time.Duration) (*PolicyClient, error) {
	if endpoint == "" || token == "" {
		return nil, errors.New("policy signer endpoint and token are both required")
	}
	client := &PolicyClient{
		endpoint: strings.TrimSuffix(endpoint, "/"),
		token:    token,
		http:     &http.Client{Timeout: timeout},
	}
	address, err := client.fetchAddress(ctx)
	if err != nil {
		return nil, err
	}
	client.address = address
	return client, nil
}

func (c *PolicyClient) Address(context.Context) (string, error) { return c.address, nil }

func (c *PolicyClient) fetchAddress(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/identity", nil)
	if err != nil {
		return "", fmt.Errorf("build policy signer identity request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("reach policy signer: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return "", fmt.Errorf("read policy signer identity: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("policy signer identity returned %d: %s",
			response.StatusCode, strings.TrimSpace(string(body)))
	}
	var decoded policy.Response
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", fmt.Errorf("decode policy signer identity: %w", err)
	}
	if decoded.SignerAddress == "" {
		return "", errors.New("policy signer reported no signing address")
	}
	return strings.ToLower(decoded.SignerAddress), nil
}

// SignTransaction asks the boundary to sign, then verifies what came back.
//
// Validate still runs locally. It is now redundant — the boundary enforces the
// same restrictions and is the one that matters — but a caller that starts
// constructing transactions this service should never sign ought to fail here
// too, where the test suite can see it, rather than only at a remote boundary.
func (c *PolicyClient) SignTransaction(ctx context.Context, tx Transaction) (SignedTransaction, error) {
	if err := tx.Validate(); err != nil {
		return SignedTransaction{}, err
	}
	if tx.Authorization == nil {
		// The boundary builds calldata from the authorization, so calldata alone
		// cannot be signed through it. Failing loudly beats sending a request the
		// boundary would reject for reasons that read as a policy violation.
		return SignedTransaction{}, errors.New("policy signer requires the authorization, not just calldata")
	}
	payload, err := json.Marshal(policy.Request{
		Nonce:                tx.Nonce,
		GasLimit:             tx.GasLimit,
		MaxFeePerGas:         tx.MaxFeePerGas,
		MaxPriorityFeePerGas: tx.MaxPriorityFeePerGas,
		Authorization:        *tx.Authorization,
	})
	if err != nil {
		return SignedTransaction{}, fmt.Errorf("encode policy signer request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/sign", bytes.NewReader(payload))
	if err != nil {
		return SignedTransaction{}, fmt.Errorf("build policy signer request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return SignedTransaction{}, fmt.Errorf("reach policy signer: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<16))
	if err != nil {
		return SignedTransaction{}, fmt.Errorf("read policy signer response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return SignedTransaction{}, fmt.Errorf("policy signer refused to sign (%d): %s",
			response.StatusCode, strings.TrimSpace(string(body)))
	}
	var decoded policy.Response
	if err := json.Unmarshal(body, &decoded); err != nil {
		return SignedTransaction{}, fmt.Errorf("decode policy signer response: %w", err)
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(decoded.RawTransaction, "0x"))
	if err != nil || len(raw) == 0 {
		return SignedTransaction{}, errors.New("policy signer returned a malformed raw transaction")
	}
	return c.verify(raw, tx)
}

// verify decodes the signed transaction and requires it to be the one that was
// asked for, then derives the sighash from the decoded fields.
//
// Deriving rather than reading it matters: the sighash is what settlement recovery
// re-signs against to prove a transaction's identity, so accepting the boundary's
// word for it would move that proof outside this process.
func (c *PolicyClient) verify(raw []byte, want Transaction) (SignedTransaction, error) {
	var signed types.Transaction
	if err := signed.UnmarshalBinary(raw); err != nil {
		return SignedTransaction{}, fmt.Errorf("decode signed transaction from policy signer: %w", err)
	}
	var errs []error
	if signed.Type() != types.DynamicFeeTxType {
		errs = append(errs, fmt.Errorf("transaction type %d is not EIP-1559", signed.Type()))
	}
	if chainID := signed.ChainId(); chainID == nil || chainID.Uint64() != want.ChainID {
		errs = append(errs, fmt.Errorf("chain id %v is not %d", chainID, want.ChainID))
	}
	if signed.Nonce() != want.Nonce {
		errs = append(errs, fmt.Errorf("nonce %d is not the allocated %d", signed.Nonce(), want.Nonce))
	}
	if to := signed.To(); to == nil || !strings.EqualFold(to.Hex(), want.To) {
		errs = append(errs, fmt.Errorf("recipient %v is not %s", to, want.To))
	}
	if signed.Value().Sign() != 0 {
		errs = append(errs, fmt.Errorf("value %s is not zero", signed.Value()))
	}
	if !bytes.Equal(signed.Data(), want.Data) {
		// The boundary rebuilt calldata from the authorization. Requiring it to
		// match what this process built from the same authorization is how the two
		// independent constructions are checked against each other in production,
		// not only in tests.
		errs = append(errs, errors.New("calldata does not match the authorization that was sent"))
	}
	if signed.Gas() != want.GasLimit {
		errs = append(errs, fmt.Errorf("gas limit %d is not %d", signed.Gas(), want.GasLimit))
	}
	if err := equalWei(signed.GasFeeCap(), want.MaxFeePerGas); err != nil {
		errs = append(errs, fmt.Errorf("max fee per gas: %w", err))
	}
	if err := equalWei(signed.GasTipCap(), want.MaxPriorityFeePerGas); err != nil {
		errs = append(errs, fmt.Errorf("max priority fee per gas: %w", err))
	}
	if err := errors.Join(errs...); err != nil {
		return SignedTransaction{}, fmt.Errorf("policy signer returned a different transaction: %w", err)
	}
	chainSigner := types.LatestSignerForChainID(new(big.Int).SetUint64(want.ChainID))
	sender, err := types.Sender(chainSigner, &signed)
	if err != nil {
		return SignedTransaction{}, fmt.Errorf("recover signer from policy signer response: %w", err)
	}
	if !strings.EqualFold(sender.Hex(), c.address) {
		// A valid signature from the wrong key. The nonce sequence in
		// signer_accounts belongs to one address, so accepting this would strand
		// every transaction behind it.
		return SignedTransaction{}, fmt.Errorf("policy signer response is signed by %s, not %s",
			strings.ToLower(sender.Hex()), c.address)
	}
	return SignedTransaction{Raw: raw, SigHash: chainSigner.Hash(&signed)}, nil
}

func equalWei(got *big.Int, want string) error {
	expected, ok := new(big.Int).SetString(want, 10)
	if !ok {
		return fmt.Errorf("%q is not a decimal wei string", want)
	}
	if got == nil || got.Cmp(expected) != 0 {
		return fmt.Errorf("%v is not %s", got, expected)
	}
	return nil
}
