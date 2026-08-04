package walletproof

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	siwe "github.com/signinwithethereum/siwe-go"
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
	if merchantID == "" || (action != "verify-recipient" && action != "change-recipient" && action != "authenticate-admin") || ttl <= 0 {
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

// Message returns the EIP-4361 human-readable challenge verified by
// VerifyMessage.
func (c Challenge) Message() string {
	return fmt.Sprintf("%s wants you to sign in with your Ethereum account:\n%s\n\nAuthorize ETH402 recipient control\n\nURI: https://%s\nVersion: 1\nChain ID: %d\nNonce: %s\nIssued At: %s\nExpiration Time: %s\nRequest ID: %s\nResources:\n- urn:eth402:action:%s",
		Domain, c.Address, Domain, c.ChainID, c.Nonce,
		c.IssuedAt.Format(time.RFC3339), c.ExpiresAt.Format(time.RFC3339), c.MerchantID, c.Action)
}

func VerifyMessage(message, signature, merchantID, expectedAddress, action string, now time.Time) error {
	parsed, err := siwe.ParseMessage(message)
	if err != nil {
		return fmt.Errorf("parse SIWE message: %w", err)
	}
	normalized, err := NormalizeAddress(expectedAddress)
	if err != nil {
		return err
	}
	if parsed.Domain != Domain || parsed.URI != "https://"+Domain || parsed.ChainID != 1 ||
		parsed.RequestID == nil || *parsed.RequestID != merchantID ||
		strings.ToLower(parsed.Address.Hex()) != normalized {
		return errors.New("SIWE challenge binding mismatch")
	}
	resource := "urn:eth402:action:" + action
	if len(parsed.Resources) != 1 || parsed.Resources[0] != resource {
		return errors.New("SIWE action binding mismatch")
	}
	issued, err := time.Parse(time.RFC3339, parsed.IssuedAt)
	if err != nil || issued.After(now.Add(time.Minute)) {
		return errors.New("invalid SIWE issue time")
	}
	if parsed.ExpirationTime == nil {
		return errors.New("SIWE expiration is required")
	}
	expires, err := time.Parse(time.RFC3339, *parsed.ExpirationTime)
	if err != nil || !now.Before(expires) {
		return errors.New("SIWE challenge expired")
	}
	if _, err := parsed.VerifyEIP191(signature); err != nil {
		return fmt.Errorf("verify SIWE signature: %w", err)
	}
	return nil
}
