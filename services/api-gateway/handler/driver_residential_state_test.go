package handler

import "testing"

func TestResidentialStateValidation(t *testing.T) {
	for _, value := range []string{"Lagos", "Abuja (FCT)", "Akwa Ibom", ""} {
		profile := identityProfile{ResidentialState: value}
		if !normalizeIdentityProfile(&profile) {
			t.Errorf("valid state rejected: %q", value)
		}
	}
	for _, value := range []string{"Vietnam", "Lekki", "Lagos State"} {
		profile := identityProfile{ResidentialState: value}
		if normalizeIdentityProfile(&profile) {
			t.Errorf("invalid state accepted: %q", value)
		}
	}
}
