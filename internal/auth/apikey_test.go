package auth

import (
	"crypto/subtle"
	"encoding/hex"
	"testing"

	"github.com/ETH402/facilitator/internal/secret"
)

func TestGenerateAPIKey(t *testing.T) {
	t.Parallel()
	pepper := []byte("01234567890123456789012345678901")
	a, err := GenerateAPIKey(pepper)
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateAPIKey(pepper)
	if err != nil {
		t.Fatal(err)
	}
	if a.FullValue == b.FullValue || a.Prefix == "" || a.Hash == a.FullValue {
		t.Fatal("key generation properties violated")
	}
	if len(a.Prefix) != len("eth402_live_")+lookupCharacters || LookupPrefix(a.FullValue) != a.Prefix {
		t.Fatal("lookup prefix does not contain the expected entropy")
	}
	got, _ := hex.DecodeString(secret.KeyedHash(pepper, a.FullValue))
	want, _ := hex.DecodeString(a.Hash)
	if subtle.ConstantTimeCompare(got, want) != 1 {
		t.Fatal("keyed hash mismatch")
	}
}
