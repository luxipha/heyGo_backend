package handler

import "testing"

func TestNormalizeGuestPassenger(t *testing.T) {
	for _, tc := range []struct {
		name, phone string
		valid       bool
	}{
		{"", "", true}, {" Amina ", " +2348012345678 ", true},
		{"Amina", "08012345678", false}, {"", "+2348012345678", false},
		{"Amina", "", false}, {"A\nmina", "+2348012345678", false},
		{"Amina", "+0001234567", false},
	} {
		n, p, ok := normalizeGuestPassenger(tc.name, tc.phone)
		if ok != tc.valid {
			t.Fatalf("unexpected validation: %q %q", tc.name, tc.phone)
		}
		if ok && tc.name != "" && (n != "Amina" || p != "+2348012345678") {
			t.Fatal("not normalized")
		}
	}
}
