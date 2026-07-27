package secret

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

func Token(bytes int) (string, error) {
	if bytes < 16 {
		return "", errors.New("token entropy must be at least 128 bits")
	}
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func Hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func KeyedHash(key []byte, value string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

func EqualHash(wantHex, value string) bool {
	want, err := hex.DecodeString(wantHex)
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(value))
	return hmac.Equal(want, sum[:])
}

func Redact(value string) string {
	if len(value) < 9 {
		return "[REDACTED]"
	}
	return value[:5] + "…" + value[len(value)-3:]
}

func Prefix(value string) string {
	if i := strings.IndexByte(value, '_'); i >= 0 && len(value) >= i+9 {
		return value[:i+9]
	}
	if len(value) > 8 {
		return value[:8]
	}
	return value
}
