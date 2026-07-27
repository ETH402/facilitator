package walletproof

import (
	"strings"
	"testing"
	"time"
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
