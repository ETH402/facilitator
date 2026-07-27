package auth

import (
	"fmt"
	"strings"

	"github.com/ETH402/facilitator/internal/secret"
)

const keyPrefix = "eth402_live_"
const lookupCharacters = 12

type GeneratedKey struct {
	FullValue string
	Prefix    string
	Hash      string
}

func GenerateAPIKey(pepper []byte) (GeneratedKey, error) {
	if len(pepper) < 32 {
		return GeneratedKey{}, fmt.Errorf("API key pepper must be at least 32 bytes")
	}
	random, err := secret.Token(32)
	if err != nil {
		return GeneratedKey{}, err
	}
	full := keyPrefix + random
	return GeneratedKey{FullValue: full, Prefix: LookupPrefix(full), Hash: secret.KeyedHash(pepper, full)}, nil
}

// LookupPrefix returns a non-secret 72-bit identifier used to select the
// candidate database row before constant-time hash comparison.
func LookupPrefix(value string) string {
	if !strings.HasPrefix(value, keyPrefix) || len(value) < len(keyPrefix)+lookupCharacters {
		return ""
	}
	return value[:len(keyPrefix)+lookupCharacters]
}
