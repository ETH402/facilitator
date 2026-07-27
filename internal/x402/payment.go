package x402

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type IdentityFields struct {
	Version     uint32
	Scheme      string
	Network     string
	Asset       string
	From        string
	To          string
	Value       string
	ValidAfter  string
	ValidBefore string
	Nonce       string
	Signature   string
}

func PaymentID(f IdentityFields) (string, error) {
	values := []string{
		strconv.FormatUint(uint64(f.Version), 10), f.Scheme, f.Network,
		strings.ToLower(f.Asset), strings.ToLower(f.From), strings.ToLower(f.To),
		f.Value, f.ValidAfter, f.ValidBefore, strings.ToLower(f.Nonce), strings.ToLower(f.Signature),
	}
	for _, value := range values {
		if value == "" {
			return "", errors.New("payment identity fields must be non-empty")
		}
	}
	h := sha256.New()
	for _, value := range values {
		if len(value) > math.MaxUint32 {
			return "", errors.New("payment identity field is too large")
		}
		var length [4]byte
		// #nosec G115 -- the explicit MaxUint32 check above proves this conversion safe.
		binary.BigEndian.PutUint32(length[:], uint32(len(value)))
		_, _ = h.Write(length[:])
		_, _ = h.Write([]byte(value))
	}
	return "pay_" + hex.EncodeToString(h.Sum(nil)), nil
}

func FormatUSDC(atomic string) (string, error) {
	if atomic == "" {
		return "", errors.New("amount is required")
	}
	for _, r := range atomic {
		if r < '0' || r > '9' {
			return "", errors.New("amount must be unsigned integer atomic units")
		}
	}
	atomic = strings.TrimLeft(atomic, "0")
	if atomic == "" {
		atomic = "0"
	}
	if len(atomic) <= 6 {
		return "0." + strings.Repeat("0", 6-len(atomic)) + atomic, nil
	}
	return fmt.Sprintf("%s.%s", atomic[:len(atomic)-6], atomic[len(atomic)-6:]), nil
}
