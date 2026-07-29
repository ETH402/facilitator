// Package policy is the signing boundary.
//
// Cloud KMS signs opaque digests: it cannot tell a USDC transfer from an ether
// withdrawal, so the calldata allowlist that ADR-0004 decision 8 describes lives
// inside the ETH402 process, where a compromise of that process bypasses it. Loss
// is then bounded only by the signer's hot balance.
//
// This package moves the allowlist behind the signing boundary. The decisive
// choice is what crosses it: **the authorization fields, never calldata and never
// a digest**. A boundary handed calldata must validate it, and a boundary handed a
// digest cannot inspect anything at all. Handed the fields, it *constructs* the
// transaction it signs — so a caller cannot express an ether transfer, a different
// contract, or a different function. The restriction is structural rather than a
// check that must be remembered.
//
// The ceilings are the boundary's own configuration, never taken from the request,
// or a compromised caller would simply raise them.
package policy

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/ETH402/facilitator/internal/config"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	x402evm "github.com/x402-foundation/x402/go/v2/mechanisms/evm"
)

// Authorization is the EIP-3009 authorization in wire form. Values are strings so
// the boundary parses them itself rather than trusting a caller's numeric encoding.
type Authorization struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Value       string `json:"value"`
	ValidAfter  string `json:"validAfter"`
	ValidBefore string `json:"validBefore"`
	Nonce       string `json:"nonce"`
	Signature   string `json:"signature"`
}

// Request asks the boundary to sign one settlement transaction.
//
// There is deliberately no calldata field, no recipient, no chain id, and no
// value: all four are implied by what this boundary exists to sign, so a caller
// has no way to name anything else.
type Request struct {
	Nonce                uint64        `json:"nonce"`
	GasLimit             uint64        `json:"gasLimit"`
	MaxFeePerGas         string        `json:"maxFeePerGas"`
	MaxPriorityFeePerGas string        `json:"maxPriorityFeePerGas"`
	Authorization        Authorization `json:"authorization"`
}

// Response returns the signed transaction and the identity that signed it.
//
// It deliberately does not return the digest. The caller needs the sighash to
// record transaction identity, but a digest taken from this response would be a
// digest the caller never derived — and settlement recovery re-signs against a
// stored sighash to prove a transaction is the recorded one, so a wrong value
// there would let a hostile boundary redirect a re-signature. The caller decodes
// the returned transaction and computes the sighash itself. Distrust runs both
// ways across this boundary.
type Response struct {
	RawTransaction string `json:"rawTransaction"`
	SignerAddress  string `json:"signerAddress"`
}

// Limits are the boundary's ceilings. They come from its own configuration.
type Limits struct {
	MaxFeePerGasWei         *big.Int
	MaxPriorityFeePerGasWei *big.Int
	MaxGasLimit             uint64
}

var (
	ErrInvalidRequest = errors.New("settlement signing request is not well formed")
	ErrOverLimit      = errors.New("settlement signing request exceeds the configured ceilings")
)

// Unsigned builds the exact transaction this boundary is willing to sign, after
// checking the request against the ceilings.
//
// Everything that determines *what* the transaction does is supplied here rather
// than by the caller: chain 1, the canonical USDC address, zero ether value, and
// transferWithAuthorization calldata built from the authorization. The caller
// influences only the nonce, the gas parameters, and the authorization contents —
// and the authorization is itself signed by the payer, so a caller cannot invent
// one that moves somebody else's USDC.
func Unsigned(request Request, limits Limits) (*types.Transaction, error) {
	if request.GasLimit == 0 {
		return nil, fmt.Errorf("%w: gas limit is required", ErrInvalidRequest)
	}
	maxFee, ok := new(big.Int).SetString(request.MaxFeePerGas, 10)
	if !ok || maxFee.Sign() <= 0 {
		return nil, fmt.Errorf("%w: max fee per gas must be a positive decimal", ErrInvalidRequest)
	}
	priorityFee, ok := new(big.Int).SetString(request.MaxPriorityFeePerGas, 10)
	if !ok || priorityFee.Sign() < 0 {
		return nil, fmt.Errorf("%w: max priority fee per gas must be an unsigned decimal", ErrInvalidRequest)
	}
	if priorityFee.Cmp(maxFee) > 0 {
		return nil, fmt.Errorf("%w: priority fee exceeds max fee", ErrInvalidRequest)
	}
	if limits.MaxGasLimit == 0 || limits.MaxFeePerGasWei == nil || limits.MaxPriorityFeePerGasWei == nil {
		// Refusing rather than defaulting: an unconfigured boundary that signs
		// anything is worse than one that signs nothing.
		return nil, fmt.Errorf("%w: boundary ceilings are not configured", ErrOverLimit)
	}
	if request.GasLimit > limits.MaxGasLimit {
		return nil, fmt.Errorf("%w: gas limit %d above %d", ErrOverLimit, request.GasLimit, limits.MaxGasLimit)
	}
	if maxFee.Cmp(limits.MaxFeePerGasWei) > 0 {
		return nil, fmt.Errorf("%w: max fee %s above %s", ErrOverLimit, maxFee, limits.MaxFeePerGasWei)
	}
	if priorityFee.Cmp(limits.MaxPriorityFeePerGasWei) > 0 {
		return nil, fmt.Errorf("%w: priority fee %s above %s", ErrOverLimit, priorityFee, limits.MaxPriorityFeePerGasWei)
	}

	calldata, err := TransferWithAuthorizationData(request.Authorization)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	to := common.HexToAddress(config.MainnetUSDC)
	return types.NewTx(&types.DynamicFeeTx{
		ChainID:   new(big.Int).SetUint64(config.MainnetChainID),
		Nonce:     request.Nonce,
		To:        &to,
		Value:     big.NewInt(0),
		Gas:       request.GasLimit,
		GasFeeCap: maxFee,
		GasTipCap: priorityFee,
		Data:      calldata,
	}), nil
}

// TransferWithAuthorizationData packs EIP-3009 v/r/s calldata from an
// authorization.
//
// This is an independent construction from the one in internal/settlement, on
// purpose. Both pack through the official ABI, and a cross-check test requires
// them to agree byte for byte: if the two ever diverge, the boundary would refuse
// to sign what settlement wants signed, or sign something else. Sharing one
// implementation would make the boundary trust code that runs inside the process
// it is protecting.
func TransferWithAuthorizationData(auth Authorization) ([]byte, error) {
	if !common.IsHexAddress(auth.From) || !common.IsHexAddress(auth.To) {
		return nil, errors.New("authorization addresses must be hex addresses")
	}
	value, ok := new(big.Int).SetString(auth.Value, 10)
	if !ok || value.Sign() <= 0 {
		return nil, fmt.Errorf("authorization value %q is not a positive decimal integer", auth.Value)
	}
	validAfter, err := strconv.ParseInt(auth.ValidAfter, 10, 64)
	if err != nil || validAfter < 0 {
		return nil, fmt.Errorf("authorization validAfter %q is not an unsigned integer", auth.ValidAfter)
	}
	validBefore, err := strconv.ParseInt(auth.ValidBefore, 10, 64)
	if err != nil || validBefore <= validAfter {
		return nil, fmt.Errorf("authorization validBefore %q must exceed validAfter", auth.ValidBefore)
	}
	nonce, err := decodeFixed32(auth.Nonce)
	if err != nil {
		return nil, fmt.Errorf("authorization nonce: %w", err)
	}
	v, r, s, err := splitSignature(auth.Signature)
	if err != nil {
		return nil, err
	}
	parsed, err := abi.JSON(strings.NewReader(string(x402evm.TransferWithAuthorizationVRSABI)))
	if err != nil {
		return nil, fmt.Errorf("parse transferWithAuthorization ABI: %w", err)
	}
	return parsed.Pack(
		x402evm.FunctionTransferWithAuthorization,
		common.HexToAddress(auth.From), common.HexToAddress(auth.To),
		value, big.NewInt(validAfter), big.NewInt(validBefore), nonce, v, r, s,
	)
}

func decodeFixed32(value string) ([32]byte, error) {
	var out [32]byte
	raw, err := hex.DecodeString(strings.TrimPrefix(value, "0x"))
	if err != nil || len(raw) != 32 {
		return out, fmt.Errorf("%q is not 0x-prefixed 32-byte hex", value)
	}
	copy(out[:], raw)
	return out, nil
}

// splitSignature accepts either recovery-id encoding and normalizes upward, for
// the same reason settlement does: the official verifier accepts 0/1, ecrecover
// requires 27/28, and rejecting the former would refuse payments that verified.
func splitSignature(signature string) (uint8, [32]byte, [32]byte, error) {
	var r, s [32]byte
	raw, err := hex.DecodeString(strings.TrimPrefix(signature, "0x"))
	if err != nil || len(raw) != 65 {
		return 0, r, s, fmt.Errorf("signature %q is not 0x-prefixed 65-byte hex", signature)
	}
	copy(r[:], raw[:32])
	copy(s[:], raw[32:64])
	switch v := raw[64]; v {
	case 0, 1:
		return v + 27, r, s, nil
	case 27, 28:
		return v, r, s, nil
	default:
		return 0, r, s, fmt.Errorf("signature recovery id %d is not 0, 1, 27, or 28", v)
	}
}
