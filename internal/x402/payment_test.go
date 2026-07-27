package x402

import "testing"

func TestPaymentIDDeterministicAndUnambiguous(t *testing.T) {
	t.Parallel()
	base := IdentityFields{2, "exact", "eip155:1", "0xasset", "0xfrom", "0xto", "100", "0", "99", "0xnonce", "0xsig"}
	a, err := PaymentID(base)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := PaymentID(base)
	if a != b {
		t.Fatal("identity not deterministic")
	}
	changed := base
	changed.Value = "10"
	changed.ValidAfter = "00"
	c, _ := PaymentID(changed)
	if a == c {
		t.Fatal("distinct structured fields collided")
	}
}

func TestFormatUSDC(t *testing.T) {
	t.Parallel()
	cases := map[string]string{"0": "0.000000", "1": "0.000001", "1000000": "1.000000", "123456789": "123.456789"}
	for input, want := range cases {
		got, err := FormatUSDC(input)
		if err != nil || got != want {
			t.Fatalf("%s: got %q, %v want %q", input, got, err, want)
		}
	}
	if _, err := FormatUSDC("-1"); err == nil {
		t.Fatal("negative amount accepted")
	}
}
