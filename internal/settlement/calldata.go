package settlement

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	x402evm "github.com/x402-foundation/x402/go/v2/mechanisms/evm"
)

// Authorization carries every field transferWithAuthorization calldata needs.
// All values come from the durable payment record, never from a request at
// broadcast time, so a worker rebuilding calldata after a crash acts on exactly
// the payment the intent committed to.
type Authorization struct {
	From        string
	To          string
	Value       string // atomic units, decimal
	ValidAfter  time.Time
	ValidBefore time.Time
	Nonce       string // 0x-prefixed 32-byte hex
	Signature   string // 0x-prefixed 65-byte hex
}

// TransferWithAuthorizationData packs the EIP-3009 v/r/s calldata for an EOA
// payer. ERC-1271 payers are rejected by verification, so the v/r/s form is
// the only one this service can ever need.
func TransferWithAuthorizationData(auth Authorization) ([]byte, error) {
	if !common.IsHexAddress(auth.From) || !common.IsHexAddress(auth.To) {
		return nil, errors.New("authorization addresses must be hex addresses")
	}
	value, ok := new(big.Int).SetString(auth.Value, 10)
	if !ok || value.Sign() <= 0 {
		return nil, fmt.Errorf("authorization value %q is not a positive decimal integer", auth.Value)
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
	data, err := parsed.Pack(
		x402evm.FunctionTransferWithAuthorization,
		common.HexToAddress(auth.From), common.HexToAddress(auth.To),
		value, new(big.Int).SetInt64(auth.ValidAfter.Unix()),
		new(big.Int).SetInt64(auth.ValidBefore.Unix()), nonce, v, r, s,
	)
	if err != nil {
		return nil, fmt.Errorf("pack transferWithAuthorization: %w", err)
	}
	return data, nil
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

// splitSignature decomposes a 65-byte signature into the v/r/s form the USDC
// contract expects. EIP-712 signatures arrive with v already normalized to
// 27/28; anything else is rejected rather than corrected, because guessing at
// recovery ids produces a transaction that burns gas to revert.
func splitSignature(signature string) (uint8, [32]byte, [32]byte, error) {
	var r, s [32]byte
	raw, err := hex.DecodeString(strings.TrimPrefix(signature, "0x"))
	if err != nil || len(raw) != 65 {
		return 0, r, s, fmt.Errorf("signature %q is not 0x-prefixed 65-byte hex", signature)
	}
	copy(r[:], raw[:32])
	copy(s[:], raw[32:64])
	v := raw[64]
	if v != 27 && v != 28 {
		return 0, r, s, fmt.Errorf("signature recovery id %d is not 27 or 28", v)
	}
	return v, r, s, nil
}
