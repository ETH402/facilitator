package x402

import "testing"

func FuzzPaymentID(f *testing.F) {
	f.Add("exact", "eip155:1", "100", "nonce")
	f.Fuzz(func(t *testing.T, scheme, network, value, nonce string) {
		fields := IdentityFields{
			Version: 2, Scheme: scheme, Network: network, Asset: "0xasset",
			From: "0xfrom", To: "0xto", Value: value, ValidAfter: "0",
			ValidBefore: "99", Nonce: nonce, Signature: "0xsig",
		}
		first, err := PaymentID(fields)
		if err != nil {
			return
		}
		second, err := PaymentID(fields)
		if err != nil || first != second {
			t.Fatalf("non-deterministic identity %q %q %v", first, second, err)
		}
	})
}
