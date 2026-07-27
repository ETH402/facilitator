package email

import (
	"testing"

	"github.com/ETH402/facilitator/internal/secret"
)

func TestVerificationToken(t *testing.T) {
	t.Parallel()
	raw, hash, err := NewVerificationToken()
	if err != nil {
		t.Fatal(err)
	}
	if raw == hash || !secret.EqualHash(hash, raw) {
		t.Fatal("token hash is not verifiable")
	}
}
