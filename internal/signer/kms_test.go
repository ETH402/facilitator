package signer

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	gax "github.com/googleapis/gax-go/v2"
)

const testKeyName = "projects/p/locations/europe-west1/keyRings/r/cryptoKeys/k/cryptoKeyVersions/1"

// fakeKMS signs like Cloud KMS (DER over the digest as given) with a locally
// generated secp256k1 key. highS forces a high-s signature to exercise the
// EIP-2 normalization path.
type fakeKMS struct {
	key     *ecdsa.PrivateKey
	highS   bool
	signErr error
}

func newFakeKMS(t *testing.T) *fakeKMS {
	t.Helper()
	key, err := ecdsa.GenerateKey(crypto.S256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeKMS{key: key}
}

func (f *fakeKMS) AsymmetricSign(_ context.Context, req *kmspb.AsymmetricSignRequest, _ ...gax.CallOption) (*kmspb.AsymmetricSignResponse, error) {
	if f.signErr != nil {
		return nil, f.signErr
	}
	digest := req.Digest.GetSha256()
	r, s, err := ecdsa.Sign(rand.Reader, f.key, digest)
	if err != nil {
		return nil, err
	}
	if f.highS {
		halfOrder := new(big.Int).Rsh(crypto.S256().Params().N, 1)
		if s.Cmp(halfOrder) <= 0 {
			s = new(big.Int).Sub(crypto.S256().Params().N, s)
		}
	}
	der, err := asn1.Marshal(struct{ R, S *big.Int }{r, s})
	if err != nil {
		return nil, err
	}
	return &kmspb.AsymmetricSignResponse{Signature: der}, nil
}

func (f *fakeKMS) GetPublicKey(context.Context, *kmspb.GetPublicKeyRequest, ...gax.CallOption) (*kmspb.PublicKey, error) {
	return &kmspb.PublicKey{Pem: pemForPublicKey(&f.key.PublicKey)}, nil
}

// pemForPublicKey marshals the SPKI PEM Cloud KMS would return — Go's x509
// has no secp256k1 support, so the structure is built by hand.
func pemForPublicKey(public *ecdsa.PublicKey) string {
	var spki struct {
		Algorithm struct {
			PublicKey asn1.ObjectIdentifier
			Curve     asn1.ObjectIdentifier
		}
		PublicKey asn1.BitString
	}
	spki.Algorithm.PublicKey = asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}
	spki.Algorithm.Curve = asn1.ObjectIdentifier{1, 3, 132, 0, 10}
	point := make([]byte, 65)
	point[0] = 0x04
	public.X.FillBytes(point[1:33])
	public.Y.FillBytes(point[33:65])
	spki.PublicKey = asn1.BitString{Bytes: point, BitLength: 520}
	der, err := asn1.Marshal(spki)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func kmsTransaction() Transaction {
	return Transaction{
		ChainID: 1, Nonce: 7,
		To:    "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
		Data:  settlementCalldata(),
		Value: "0", GasLimit: 120000,
		MaxFeePerGas: "30000000000", MaxPriorityFeePerGas: "2000000000",
	}
}

func TestCloudKMSAddressDerivesFromPublicKey(t *testing.T) {
	fake := newFakeKMS(t)
	backend, err := NewCloudKMS(context.Background(), fake, testKeyName)
	if err != nil {
		t.Fatalf("NewCloudKMS: %v", err)
	}
	address, err := backend.Address(context.Background())
	if err != nil {
		t.Fatalf("Address: %v", err)
	}
	want := strings.ToLower(crypto.PubkeyToAddress(fake.key.PublicKey).Hex())
	if address != want {
		t.Fatalf("address = %s, want %s", address, want)
	}
}

func TestCloudKMSSignsRecoverableTransaction(t *testing.T) {
	for _, highS := range []bool{false, true} {
		fake := newFakeKMS(t)
		fake.highS = highS
		backend, err := NewCloudKMS(context.Background(), fake, testKeyName)
		if err != nil {
			t.Fatalf("NewCloudKMS: %v", err)
		}
		signed, err := backend.SignTransaction(context.Background(), kmsTransaction())
		if err != nil {
			t.Fatalf("highS=%v sign: %v", highS, err)
		}
		parsed := new(types.Transaction)
		if err := parsed.UnmarshalBinary(signed.Raw); err != nil {
			t.Fatalf("highS=%v unmarshal: %v", highS, err)
		}
		from, err := types.Sender(types.LatestSignerForChainID(big.NewInt(1)), parsed)
		if err != nil {
			t.Fatalf("highS=%v recover sender: %v", highS, err)
		}
		if from != crypto.PubkeyToAddress(fake.key.PublicKey) {
			t.Fatalf("highS=%v sender = %s", highS, from.Hex())
		}
		// EIP-2: the emitted s must sit in the lower half-order.
		_, _, s := parsed.RawSignatureValues()
		halfOrder := new(big.Int).Rsh(crypto.S256().Params().N, 1)
		if s.Cmp(halfOrder) > 0 {
			t.Fatalf("highS=%v s exceeds half-order", highS)
		}
	}
}

// TestBackendsAgreeOnSigHash pins the property settlement recovery relies on:
// the sighash is fully determined by the transaction fields, so every backend
// reports the identical digest for the identical transaction — even though the
// signed bytes differ, because a KMS-style signer randomizes the ECDSA nonce
// (fakeKMS signs with a random k, like Cloud KMS) while Development is
// deterministic (RFC 6979).
func TestBackendsAgreeOnSigHash(t *testing.T) {
	dev, err := NewDevelopment("0x" + strings.Repeat("00", 31) + "01")
	if err != nil {
		t.Fatalf("development signer: %v", err)
	}
	backend, err := NewCloudKMS(context.Background(), newFakeKMS(t), testKeyName)
	if err != nil {
		t.Fatalf("NewCloudKMS: %v", err)
	}
	tx := kmsTransaction()
	devSigned, err := dev.SignTransaction(context.Background(), tx)
	if err != nil {
		t.Fatalf("development sign: %v", err)
	}
	kmsSigned1, err := backend.SignTransaction(context.Background(), tx)
	if err != nil {
		t.Fatalf("KMS sign 1: %v", err)
	}
	kmsSigned2, err := backend.SignTransaction(context.Background(), tx)
	if err != nil {
		t.Fatalf("KMS sign 2: %v", err)
	}
	if devSigned.SigHash == ([32]byte{}) {
		t.Fatal("development signer returned a zero sighash")
	}
	if devSigned.SigHash != kmsSigned1.SigHash || kmsSigned1.SigHash != kmsSigned2.SigHash {
		t.Fatalf("sighash differs across backends/signings: dev %x, kms %x / %x",
			devSigned.SigHash, kmsSigned1.SigHash, kmsSigned2.SigHash)
	}
	if bytes.Equal(kmsSigned1.Raw, kmsSigned2.Raw) {
		t.Fatal("expected randomized-k signatures to differ; the test no longer models Cloud KMS")
	}
}

func TestCloudKMSRejectsUnsafeTransaction(t *testing.T) {
	fake := newFakeKMS(t)
	backend, err := NewCloudKMS(context.Background(), fake, testKeyName)
	if err != nil {
		t.Fatalf("NewCloudKMS: %v", err)
	}
	tx := kmsTransaction()
	tx.Value = "1"
	if _, err := backend.SignTransaction(context.Background(), tx); err == nil {
		t.Fatal("signed a non-zero-value transaction")
	}
}

func TestCloudKMSSignFailurePropagates(t *testing.T) {
	fake := newFakeKMS(t)
	fake.signErr = errors.New("kms unavailable")
	backend, err := NewCloudKMS(context.Background(), fake, testKeyName)
	if err != nil {
		t.Fatalf("NewCloudKMS: %v", err)
	}
	if _, err := backend.SignTransaction(context.Background(), kmsTransaction()); err == nil ||
		!strings.Contains(err.Error(), "kms unavailable") {
		t.Fatalf("err = %v", err)
	}
}

func TestCloudKMSRejectsMismatchedSignature(t *testing.T) {
	fake := newFakeKMS(t)
	backend, err := NewCloudKMS(context.Background(), fake, testKeyName)
	if err != nil {
		t.Fatalf("NewCloudKMS: %v", err)
	}
	// A different key signs than the public key advertised: no recovery id can
	// reproduce the address, which must fail rather than emit garbage.
	other := newFakeKMS(t)
	backend.api = other
	if _, err := backend.SignTransaction(context.Background(), kmsTransaction()); err == nil ||
		!strings.Contains(err.Error(), "no recovery id") {
		t.Fatalf("err = %v", err)
	}
}

func TestAddressFromPEMRejectsGarbage(t *testing.T) {
	if _, err := addressFromPEM("not pem"); err == nil {
		t.Fatal("accepted a non-PEM public key")
	}
	if _, err := addressFromPEM(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("junk")}))); err == nil {
		t.Fatal("accepted an unparseable public key")
	}
}
