package signer

import (
	"context"
	"crypto/ecdsa"
	"encoding/asn1"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"

	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	gax "github.com/googleapis/gax-go/v2"
	"google.golang.org/api/option"
)

// CloudKMS signs with a GCP Cloud KMS EC_SIGN_SECP256K1_SHA256 key: the key
// material never leaves KMS, and every signature is a network round trip
// (ADR-0004 decision 8). The caller's signing timeout bounds that round trip.
//
// KMS signs digests and cannot inspect calldata — the digest field carries
// the Ethereum sighash, and the calldata allowlist lives in
// Transaction.Validate inside ETH402, not in the signing boundary.
type CloudKMS struct {
	api     KMSAPI
	keyName string
	address common.Address
}

// KMSAPI is the subset of the Cloud KMS client the signer uses. It exists so
// tests can run without GCP; *kms.KeyManagementClient satisfies it.
type KMSAPI interface {
	AsymmetricSign(ctx context.Context, req *kmspb.AsymmetricSignRequest, opts ...gax.CallOption) (*kmspb.AsymmetricSignResponse, error)
	GetPublicKey(ctx context.Context, req *kmspb.GetPublicKeyRequest, opts ...gax.CallOption) (*kmspb.PublicKey, error)
}

// NewCloudKMS resolves the signer's address from the key's public key at
// construction, so a misconfigured key name or missing permission fails at
// startup rather than on the first settlement. keyName must name a key
// version: projects/*/locations/*/keyRings/*/cryptoKeys/*/cryptoKeyVersions/*.
func NewCloudKMS(ctx context.Context, api KMSAPI, keyName string) (*CloudKMS, error) {
	public, err := api.GetPublicKey(ctx, &kmspb.GetPublicKeyRequest{Name: keyName})
	if err != nil {
		return nil, fmt.Errorf("fetch KMS public key: %w", err)
	}
	address, err := addressFromPEM(public.Pem)
	if err != nil {
		return nil, fmt.Errorf("parse KMS public key: %w", err)
	}
	return &CloudKMS{api: api, keyName: keyName, address: address}, nil
}

// NewCloudKMSClient builds the production KMSAPI from Application Default
// Credentials. The caller closes the client.
func NewCloudKMSClient(ctx context.Context, opts ...option.ClientOption) (*kms.KeyManagementClient, error) {
	client, err := kms.NewKeyManagementClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("create Cloud KMS client: %w", err)
	}
	return client, nil
}

// Address returns the signer's address lowercased, matching the database
// normalization for signer_accounts and ethereum_transactions.
func (s *CloudKMS) Address(context.Context) (string, error) {
	return strings.ToLower(s.address.Hex()), nil
}

func (s *CloudKMS) SignTransaction(ctx context.Context, tx Transaction) (SignedTransaction, error) {
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
	chainSigner := types.LatestSignerForChainID(new(big.Int).SetUint64(tx.ChainID))
	sighash := chainSigner.Hash(unsigned)
	// KMS signs the digest as given without re-hashing, so the Ethereum
	// keccak sighash travels in the sha256 digest field.
	response, err := s.api.AsymmetricSign(ctx, &kmspb.AsymmetricSignRequest{
		Name:   s.keyName,
		Digest: &kmspb.Digest{Digest: &kmspb.Digest_Sha256{Sha256: sighash[:]}},
	})
	if err != nil {
		return SignedTransaction{}, fmt.Errorf("KMS asymmetric sign: %w", err)
	}
	signature, err := ethereumSignature(sighash, response.Signature, s.address)
	if err != nil {
		return SignedTransaction{}, err
	}
	signed, err := unsigned.WithSignature(chainSigner, signature)
	if err != nil {
		return SignedTransaction{}, fmt.Errorf("attach signature: %w", err)
	}
	raw, err := signed.MarshalBinary()
	if err != nil {
		return SignedTransaction{}, fmt.Errorf("encode signed transaction: %w", err)
	}
	return SignedTransaction{Raw: raw}, nil
}

// ethereumSignature converts a DER ECDSA signature from KMS into the 65-byte
// Ethereum form (r ‖ s ‖ v): s is normalized to the lower half-order (EIP-2)
// and v is found by recovering the expected address.
func ethereumSignature(sighash [32]byte, der []byte, expected common.Address) ([]byte, error) {
	var parsed struct{ R, S *big.Int }
	if _, err := asn1.Unmarshal(der, &parsed); err != nil {
		return nil, fmt.Errorf("decode DER signature: %w", err)
	}
	halfOrder := new(big.Int).Rsh(crypto.S256().Params().N, 1)
	s := parsed.S
	if s.Cmp(halfOrder) > 0 {
		s = new(big.Int).Sub(crypto.S256().Params().N, s)
	}
	signature := make([]byte, 65)
	parsed.R.FillBytes(signature[:32])
	s.FillBytes(signature[32:64])
	for _, v := range []byte{0, 1} {
		signature[64] = v
		public, err := crypto.SigToPub(sighash[:], signature)
		if err != nil {
			continue
		}
		if crypto.PubkeyToAddress(*public) == expected {
			return signature, nil
		}
	}
	return nil, fmt.Errorf("no recovery id reproduces signer address %s", expected.Hex())
}

// addressFromPEM derives the Ethereum address from the PEM SubjectPublicKeyInfo
// Cloud KMS returns. Go's x509 has no secp256k1 support, so the SPKI is parsed
// directly: the OID pair is asserted and the uncompressed point decoded by hand.
func addressFromPEM(pemKey string) (common.Address, error) {
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return common.Address{}, fmt.Errorf("no PEM block in public key")
	}
	var key struct {
		Algorithm struct {
			PublicKey asn1.ObjectIdentifier
			Curve     asn1.ObjectIdentifier
		}
		PublicKey asn1.BitString
	}
	if _, err := asn1.Unmarshal(block.Bytes, &key); err != nil {
		return common.Address{}, fmt.Errorf("parse public key: %w", err)
	}
	if !key.Algorithm.PublicKey.Equal(oidECPublicKey) || !key.Algorithm.Curve.Equal(oidSecp256k1) {
		return common.Address{}, fmt.Errorf("public key algorithm %s/%s is not secp256k1",
			key.Algorithm.PublicKey, key.Algorithm.Curve)
	}
	point := key.PublicKey.Bytes
	if len(point) != 65 || point[0] != 0x04 {
		return common.Address{}, fmt.Errorf("public key is not an uncompressed secp256k1 point")
	}
	x := new(big.Int).SetBytes(point[1:33])
	y := new(big.Int).SetBytes(point[33:65])
	if !crypto.S256().IsOnCurve(x, y) {
		return common.Address{}, fmt.Errorf("public key point is not on secp256k1")
	}
	return crypto.PubkeyToAddress(ecdsa.PublicKey{Curve: crypto.S256(), X: x, Y: y}), nil
}

var (
	oidECPublicKey = asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}
	oidSecp256k1   = asn1.ObjectIdentifier{1, 3, 132, 0, 10}
)
