package auth

import (
	"fmt"

	"github.com/ETH402/facilitator/internal/secret"
)

const keyPrefix = "eth402_live_"

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
	return GeneratedKey{FullValue: full, Prefix: secret.Prefix(full), Hash: secret.KeyedHash(pepper, full)}, nil
}
