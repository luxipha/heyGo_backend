package handler

import "testing"

func TestValidTripPreferences(t *testing.T) {
	for _, tc := range []struct {
		p     map[string]string
		valid bool
	}{
		{nil, false}, {map[string]string{}, true},
		{map[string]string{"conversation": "quiet", "air": "ac_on", "music": "off"}, true},
		{map[string]string{"conversation": "quiet"}, false},
		{map[string]string{"conversation": "quiet", "air": "guaranteed_ac", "music": "off"}, false},
		{map[string]string{"conversation": "quiet", "air": "either", "music": "off", "pets": "yes"}, false},
	} {
		if validTripPreferences(tc.p) != tc.valid {
			t.Fatalf("unexpected validation for %#v", tc.p)
		}
	}
}
