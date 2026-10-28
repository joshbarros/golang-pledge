// Package donations is the HTTP side of the saga and the handler that moves a donation forward as events arrive.
package donations

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// Donation rules.
const (
	// MaxDonationCents is $50,000.00 per donation.
	MaxDonationCents   = 5_000_000
	MaxReferenceLength = 40
	FlatFeeCents       = 50
	FeeBasisPoints     = 50
)

// ErrOverflow is returned instead of wrapping.
var ErrOverflow = errors.New("arithmetic overflow")

// Validate returns an error message, or "" when the request is acceptable.
func Validate(reference string, amountCents int64) string {
	trimmed := strings.TrimSpace(reference)
	switch {
	case trimmed == "":
		return "Donation reference is required."
	case len([]rune(trimmed)) > MaxReferenceLength:
		return fmt.Sprintf("Donation reference must be at most %d characters.", MaxReferenceLength)
	case amountCents < 1:
		return "A donation needs at least 1 cent."
	case amountCents > MaxDonationCents:
		return fmt.Sprintf("A donation is at most %d cents ($50,000.00).", MaxDonationCents)
	}
	return ""
}

// Fee is a flat 50 cents plus 0.5% of the amount, truncated to a whole cent. Overflow is an error, not a wrap.
func Fee(amountCents int64) (int64, error) {
	if amountCents < 0 || amountCents > math.MaxInt64/FeeBasisPoints {
		return 0, ErrOverflow
	}
	return FlatFeeCents + amountCents*FeeBasisPoints/10_000, nil
}
