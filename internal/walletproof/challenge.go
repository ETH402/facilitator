package walletproof

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/sha3"
)

const Domain = "eth402.org"

type Challenge struct {
	MerchantID string
	Address    string
	Nonce      string
	IssuedAt   time.Time
	ExpiresAt  time.Time
	Action     string
	ChainID    uint64
}

func NormalizeAddress(address string) (string, error) {
	if len(address) != 42 || !strings.HasPrefix(address, "0x") {
		return "", errors.New("ethereum address must be 20-byte 0x-prefixed hex")
	}
	if _, err := hex.DecodeString(address[2:]); err != nil {
		return "", errors.New("ethereum address contains invalid hex")
	}
	if strings.EqualFold(address, "0x0000000000000000000000000000000000000000") {
		return "", errors.New("zero address is not permitted")
	}
	return strings.ToLower(address), nil
}

func ChecksumAddress(address string) (string, error) {
	normalized, err := NormalizeAddress(address)
	if err != nil {
		return "", err
	}
	lower := normalized[2:]
	hasher := sha3.NewLegacyKeccak256()
	_, _ = hasher.Write([]byte(lower))
	digest := hasher.Sum(nil)
	result := []byte(lower)
	for i := range result {
		if result[i] >= 'a' && result[i] <= 'f' {
			nibble := digest[i/2]
			if i%2 == 0 {
				nibble >>= 4
			} else {
				nibble &= 0x0f
			}
			if nibble >= 8 {
				result[i] -= 'a' - 'A'
			}
		}
	}
	return "0x" + string(result), nil
}

func NewChallenge(merchantID, address, action string, now time.Time, ttl time.Duration) (Challenge, error) {
	checksummed, err := ChecksumAddress(address)
	if err != nil {
		return Challenge{}, err
	}
	if merchantID == "" || (action != "verify-recipient" && action != "change-recipient") || ttl <= 0 {
		return Challenge{}, errors.New("invalid wallet challenge parameters")
	}
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return Challenge{}, err
	}
	now = now.UTC().Truncate(time.Second)
	return Challenge{
		MerchantID: merchantID, Address: checksummed, Nonce: hex.EncodeToString(nonceBytes),
		IssuedAt: now, ExpiresAt: now.Add(ttl), Action: action, ChainID: 1,
	}, nil
}

// Message returns an EIP-4361-style human-readable challenge. Signature
// verification is intentionally deferred to Milestone 1.
func (c Challenge) Message() string {
	return fmt.Sprintf("%s wants you to sign in with your Ethereum account:\n%s\n\nAuthorize ETH402 recipient control\n\nURI: https://%s\nVersion: 1\nChain ID: %d\nNonce: %s\nIssued At: %s\nExpiration Time: %s\nRequest ID: %s\nResources:\n- urn:eth402:action:%s",
		Domain, c.Address, Domain, c.ChainID, c.Nonce,
		c.IssuedAt.Format(time.RFC3339), c.ExpiresAt.Format(time.RFC3339), c.MerchantID, c.Action)
}
