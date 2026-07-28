package signer

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// Development signs with a raw private key held in process memory. It exists
// for local development against Anvil and for integration tests. Production
// configuration rejects it unless ETH402_ALLOW_UNSAFE_PRODUCTION_SIGNER is
// explicitly set; the production backend is Cloud KMS (ADR-0004 decision 8).
type Development struct {
	key     *ecdsa.PrivateKey
	address common.Address
}

// NewDevelopment parses a 0x-prefixed hex private key. The key material is the
// caller's secret; this package never logs it.
func NewDevelopment(privateKeyHex string) (*Development, error) {
	key, err := crypto.HexToECDSA(trimPrefix(privateKeyHex))
	if err != nil {
		return nil, fmt.Errorf("parse development signer key: %w", err)
	}
	return &Development{key: key, address: crypto.PubkeyToAddress(key.PublicKey)}, nil
}

// Address returns the signer's address lowercased, matching the database
// normalization for signer_accounts and ethereum_transactions.
func (s *Development) Address(context.Context) (string, error) {
	return strings.ToLower(s.address.Hex()), nil
}

func (s *Development) SignTransaction(_ context.Context, tx Transaction) (SignedTransaction, error) {
	if err := tx.Validate(); err != nil {
		return SignedTransaction{}, err
	}
	maxFee, _ := new(big.Int).SetString(tx.MaxFeePerGas, 10)
	priorityFee, _ := new(big.Int).SetString(tx.MaxPriorityFeePerGas, 10)
	to := common.HexToAddress(tx.To)
	unsigned := types.NewTx(&types.DynamicFeeTx{
		ChainID:   new(big.Int).SetUint64(tx.ChainID),
		Nonce:     tx.Nonce,
		To:        &to,
		Value:     big.NewInt(0),
		Gas:       tx.GasLimit,
		GasFeeCap: maxFee,
		GasTipCap: priorityFee,
		Data:      tx.Data,
	})
	signed, err := types.SignTx(unsigned, types.LatestSignerForChainID(new(big.Int).SetUint64(tx.ChainID)), s.key)
	if err != nil {
		return SignedTransaction{}, fmt.Errorf("sign transaction: %w", err)
	}
	raw, err := signed.MarshalBinary()
	if err != nil {
		return SignedTransaction{}, fmt.Errorf("encode signed transaction: %w", err)
	}
	return SignedTransaction{Raw: raw}, nil
}

func trimPrefix(value string) string {
	if len(value) >= 2 && value[:2] == "0x" {
		return value[2:]
	}
	return value
}
