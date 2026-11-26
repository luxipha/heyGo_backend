package handler

import "testing"

func TestCanOverrideInspection(t *testing.T) {
	for role, want := range map[string]bool{"super_admin": true, "compliance": true, "support": false, "finance": false, "onboarding": false, "": false} {
		if got := canOverrideInspection(adminIdentity{Role: role}); got != want {
			t.Errorf("role %q: got %v, want %v", role, got, want)
		}
	}
}
