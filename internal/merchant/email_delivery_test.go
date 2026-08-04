package merchant

import (
	"bytes"
	"testing"
)

func TestEmailTokenCiphertextBindsKeyAndRowIdentity(t *testing.T) {
	t.Parallel()
	key := normalizeEmailOutboxKey(bytes.Repeat([]byte{0x11}, 32))
	otherKey := normalizeEmailOutboxKey(bytes.Repeat([]byte{0x22}, 32))
	const (
		token      = "secret-one-time-token"
		merchantID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		tokenHash  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		kind       = "admin_login"
	)
	ciphertext, err := sealEmailToken(key, token, merchantID, tokenHash, kind)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := openEmailToken(key, ciphertext, merchantID, tokenHash, kind)
	if err != nil || plain != token {
		t.Fatalf("round trip = %q, %v", plain, err)
	}
	cases := []struct {
		name                   string
		key                    [32]byte
		ciphertext             []byte
		merchantID, hash, kind string
	}{
		{name: "wrong key", key: otherKey, ciphertext: ciphertext, merchantID: merchantID, hash: tokenHash, kind: kind},
		{name: "wrong merchant", key: key, ciphertext: ciphertext, merchantID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", hash: tokenHash, kind: kind},
		{name: "swapped token hash", key: key, ciphertext: ciphertext, merchantID: merchantID, hash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", kind: kind},
		{name: "swapped message kind", key: key, ciphertext: ciphertext, merchantID: merchantID, hash: tokenHash, kind: "registration"},
	}
	tampered := append([]byte(nil), ciphertext...)
	tampered[len(tampered)-1] ^= 1
	cases = append(cases, struct {
		name                   string
		key                    [32]byte
		ciphertext             []byte
		merchantID, hash, kind string
	}{name: "tampered ciphertext", key: key, ciphertext: tampered, merchantID: merchantID, hash: tokenHash, kind: kind})
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := openEmailToken(test.key, test.ciphertext, test.merchantID, test.hash, test.kind); err == nil {
				t.Fatal("ciphertext opened with mismatched or tampered inputs")
			}
		})
	}
}

func TestEmailRetryDelayIsBounded(t *testing.T) {
	t.Parallel()
	if got := retryDelay(1); got != emailRetryMinimum {
		t.Fatalf("first retry = %s, want %s", got, emailRetryMinimum)
	}
	if got := retryDelay(100); got != emailRetryMaximum {
		t.Fatalf("maximum retry = %s, want %s", got, emailRetryMaximum)
	}
}
