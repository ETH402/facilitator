package walletproof

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	siwe "github.com/signinwithethereum/siwe-go"
)

func TestChallengeBindsSecurityContext(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	c, err := NewChallenge("merchant-123", "0x1111111111111111111111111111111111111111", "verify-recipient", now, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{Domain, c.MerchantID, c.Address, c.Nonce, "Chain ID: 1", "verify-recipient", now.Format(time.RFC3339)} {
		if !strings.Contains(c.Message(), value) {
			t.Fatalf("challenge does not bind %q", value)
		}
	}
}

func TestAdminChallengeUsesDistinctAction(t *testing.T) {
	t.Parallel()
	c, err := NewChallenge("merchant-123", "0x1111111111111111111111111111111111111111",
		"authenticate-admin", time.Now().UTC(), 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Message(), "urn:eth402:action:authenticate-admin") {
		t.Fatalf("admin action missing from challenge: %s", c.Message())
	}
	if _, err := NewChallenge("merchant-123", c.Address, "admin", time.Now(), time.Minute); err == nil {
		t.Fatal("unknown challenge action accepted")
	}
}

func TestVerifyMessage(t *testing.T) {
	t.Parallel()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	address := crypto.PubkeyToAddress(key.PublicKey).Hex()
	now := time.Now().UTC().Truncate(time.Second)
	challenge, err := NewChallenge("merchant-123", address, "verify-recipient", now, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := siwe.ParseMessage(challenge.Message())
	if err != nil {
		t.Fatal(err)
	}
	sig, err := crypto.Sign(parsed.EIP191Hash().Bytes(), key)
	if err != nil {
		t.Fatal(err)
	}
	sig[64] += 27
	signature := "0x" + hex.EncodeToString(sig)
	if err := VerifyMessage(challenge.Message(), signature, "merchant-123", address, "verify-recipient", now); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if err := VerifyMessage(challenge.Message()+" ", signature, "merchant-123", address, "verify-recipient", now); err == nil {
		t.Fatal("modified challenge accepted")
	}
	if err := VerifyMessage(challenge.Message(), signature, "wrong-merchant", address, "verify-recipient", now); err == nil {
		t.Fatal("wrong merchant binding accepted")
	}
	if err := VerifyMessage(challenge.Message(), "0xdeadbeef", "merchant-123", address, "verify-recipient", now); err == nil {
		t.Fatal("malformed signature accepted")
	}
	wrongKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	wrongSignature, err := crypto.Sign(parsed.EIP191Hash().Bytes(), wrongKey)
	if err != nil {
		t.Fatal(err)
	}
	wrongSignature[64] += 27
	if err := VerifyMessage(challenge.Message(), "0x"+hex.EncodeToString(wrongSignature), "merchant-123", address, "verify-recipient", now); err == nil {
		t.Fatal("wrong signer accepted")
	}
	if err := VerifyMessage(challenge.Message(), signature, "merchant-123", address, "verify-recipient", now.Add(11*time.Minute)); err == nil {
		t.Fatal("expired signature accepted")
	}
}

func TestNormalizeAddress(t *testing.T) {
	t.Parallel()
	got, err := NormalizeAddress("0xAa11111111111111111111111111111111111111")
	if err != nil || got != "0xaa11111111111111111111111111111111111111" {
		t.Fatalf("normalization failed: %q %v", got, err)
	}
	if _, err := NormalizeAddress("0x0000000000000000000000000000000000000000"); err == nil {
		t.Fatal("zero address accepted")
	}
}

func TestChecksumAddress(t *testing.T) {
	t.Parallel()
	got, err := ChecksumAddress("0x52908400098527886e0f7030069857d2e4169ee7")
	if err != nil {
		t.Fatal(err)
	}
	if got != "0x52908400098527886E0F7030069857D2E4169EE7" {
		t.Fatalf("unexpected EIP-55 address %s", got)
	}
}
