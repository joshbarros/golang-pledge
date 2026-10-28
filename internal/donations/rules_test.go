package donations

import (
	"math"
	"strings"
	"testing"
)

func TestValidateRules(t *testing.T) {
	long := strings.Repeat("x", MaxReferenceLength+1)
	cases := []struct {
		reference string
		amount    int64
		want      string
	}{
		{"INV-1", 100, ""},
		{"  INV-1  ", 1, ""},
		{strings.Repeat("x", MaxReferenceLength), MaxDonationCents, ""},
		{"", 100, "Donation reference is required."},
		{"   ", 100, "Donation reference is required."},
		{long, 100, "Donation reference must be at most 40 characters."},
		{"INV-1", 0, "A donation needs at least 1 cent."},
		{"INV-1", -5, "A donation needs at least 1 cent."},
		{"INV-1", MaxDonationCents + 1, "A donation is at most 5000000 cents ($50,000.00)."},
		{"INV-1", math.MaxInt64, "A donation is at most 5000000 cents ($50,000.00)."},
		{"INV-1", 4294967297, "A donation is at most 5000000 cents ($50,000.00)."},
	}
	for _, c := range cases {
		if got := Validate(c.reference, c.amount); got != c.want {
			t.Errorf("Validate(%q, %d) = %q, want %q", c.reference, c.amount, got, c.want)
		}
	}
}

func TestFeeIsAFlatFiftyCentsPlusAHalfPercentTruncated(t *testing.T) {
	cases := map[int64]int64{1: 50, 199: 50, 200: 51, 30000: 200, 100000: 550, MaxDonationCents: 25050}
	for amount, want := range cases {
		got, err := Fee(amount)
		if err != nil || got != want {
			t.Errorf("Fee(%d) = %d, %v; want %d", amount, got, err, want)
		}
	}
}

func TestFeeThrowsInsteadOfWrapping(t *testing.T) {
	for _, amount := range []int64{math.MaxInt64, math.MaxInt64 / 2, math.MaxInt64/FeeBasisPoints + 1, -1} {
		if _, err := Fee(amount); err == nil {
			t.Errorf("Fee(%d) must overflow", amount)
		}
	}
}
