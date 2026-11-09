package handler

import "testing"

func TestNormalizeVIN(t *testing.T) {
	for raw, want := range map[string]string{
		" jtd-kb20u 0938 47512 ": "JTDKB20U093847512",
		"knadn5.12345":           "KNADN512345",
		"":                       "",
	} {
		if got := normalizeVIN(raw); got != want {
			t.Fatalf("normalizeVIN(%q)=%q want %q", raw, got, want)
		}
	}
	for value, valid := range map[string]bool{"JTDKB20U093847512": true, "AB12": false, "ABC$123": false, "KNADN512345": true} {
		if vinPattern.MatchString(normalizeVIN(value)) != valid {
			t.Fatalf("vinPattern(%q) want %v", value, valid)
		}
	}
}
