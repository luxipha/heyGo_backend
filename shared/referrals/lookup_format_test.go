package referrals

import (
	"context"
	"errors"
	"testing"
)

// Format rejection happens before any database access, so a nil querier is safe.
func TestLookupRejectsMalformedCodes(t *testing.T) {
	for _, code := range []string{"", "GO", "https://heygo.ng/ref/GO-ABC", "GO-ABC DEF", "Code: GO-ABC", string(make([]byte, 40))} {
		_, err := Lookup(context.Background(), nil, code, "rider")
		if !errors.Is(err, ErrBadFormat) || !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: got %v", code, err)
		}
	}
}
