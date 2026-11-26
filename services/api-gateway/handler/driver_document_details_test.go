package handler

import (
	"strings"
	"testing"
)

func TestCleanDocumentDetails(t *testing.T) {
	got, ok := cleanDocumentDetails("driver_license", map[string]string{"licenseNumber": "  LAG12345AA01 ", "licenseClass": "D"})
	if !ok || got["licenseNumber"] != "LAG12345AA01" || got["licenseClass"] != "D" {
		t.Fatalf("valid details rejected or not trimmed: %v %v", got, ok)
	}
	if got, ok := cleanDocumentDetails("driver_license", nil); !ok || got != nil {
		t.Fatalf("no details must be accepted: %v %v", got, ok)
	}
	rejected := []struct {
		name    string
		kind    string
		details map[string]string
	}{
		{"unknown key", "driver_license", map[string]string{"nin": "12345678901"}},
		{"key of another type", "driver_license", map[string]string{"plateNumber": "ABC123XY"}},
		{"empty value", "roadworthiness", map[string]string{"certificateNumber": "   "}},
		{"too long", "auto_insurance", map[string]string{"policyNumber": strings.Repeat("x", 121)}},
		{"control character", "hackney_permit", map[string]string{"permitNumber": "HP\n1"}},
		{"unsupported type", "lasdri", map[string]string{"licenseNumber": "1"}},
	}
	for _, tc := range rejected {
		if _, ok := cleanDocumentDetails(tc.kind, tc.details); ok {
			t.Errorf("%s: expected rejection", tc.name)
		}
	}
	if _, ok := cleanDocumentDetails("auto_insurance", map[string]string{"insurer": "AXA Mansard", "coverType": "Comprehensive", "policyNumber": "POL/1"}); !ok {
		t.Error("insurance details should be accepted")
	}
}

func TestAdminDocumentFieldsUseAdminKeys(t *testing.T) {
	got := adminDocumentFields(map[string]string{"licenseNumber": "L1", "licenseClass": "D", "insurer": "AXA", "policyNumber": "P1", "coverType": "Comprehensive", "holderName": "A B"})
	want := map[string]string{"licenceNumber": "L1", "licenceClass": "D", "insuranceProvider": "AXA", "policyNumber": "P1", "coverType": "Comprehensive", "holderName": "A B"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s: got %q, want %q", key, got[key], value)
		}
	}
}
