package handler

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAdminRoutePermissionsDenyUnmappedAndSeparateOwners(t *testing.T) {
	cases := []struct{ method, path, want string }{
		{http.MethodGet, "/admin/catalogue", permissionFinanceRead},
		{http.MethodPost, "/admin/catalogue/categories", permissionFinanceWrite},
		{http.MethodPost, "/admin/catalogue/vehicles/:id/status", permissionFinanceWrite},
		{http.MethodGet, "/admin/drivers/:driverID/catalogue", permissionDriverRead},
		{http.MethodPost, "/admin/drivers/:driverID/catalogue/vehicles", permissionDriverWrite},
		{http.MethodGet, "/admin/support/bug-reports", permissionSupportRead},
		{http.MethodGet, "/admin/support/bug-reports/:reportID", permissionSupportRead},
		{http.MethodPost, "/admin/support/bug-reports/:reportID/comments", permissionSupportWrite},
		{http.MethodPatch, "/admin/support/bug-reports/:reportID", permissionSupportWrite},
		{http.MethodGet, "/admin/drivers/:driverID/documents/:documentID/files/:side", permissionDriverRead},
		{http.MethodGet, "/admin/drivers/:driverID/verification-timeline", permissionDriverRead},
		{http.MethodPost, "/admin/drivers/:driverID/approval", permissionDriverWrite},
		{http.MethodPost, "/admin/account-deletion-requests/:requestID/review", permissionDriverWrite},
		{http.MethodGet, "/admin/driver-ratings", permissionSafetyRead},
		{http.MethodPost, "/admin/driver-ratings/:tripID/review", permissionSafetyWrite},
		{http.MethodGet, "/admin/operating-balance/policies", permissionFinanceRead},
		{http.MethodPost, "/admin/no-show/claims/:id/review", permissionFinanceWrite},
		{http.MethodGet, "/admin/no-show/claims/:id", permissionFinanceRead},
		{http.MethodPost, "/admin/geofences/:id/approve", permissionGeofenceWrite},
		{http.MethodPost, "/admin/geofences/preview", permissionGeofenceWrite},
		{http.MethodGet, "/admin/geofences/pending-approvals", permissionGeofenceRead},
		{http.MethodGet, "/admin/pricing/:id/decision-receipt", permissionFinanceRead},
		{http.MethodPost, "/admin/pricing/:id/approve", permissionFinanceWrite},
		{http.MethodPost, "/admin/support/cases/:caseID/messages", permissionSupportWrite},
		{http.MethodGet, "/admin/support/cases/:caseID/contact", permissionSupportContact},
		{http.MethodPut, "/admin/settings/resend", permissionSettings},
		{http.MethodGet, "/admin/trips/:tripID", permissionTripRead},
		{http.MethodGet, "/admin/riders/:riderID", permissionRiderRead},
		{http.MethodPost, "/admin/riders/:riderID/restrictions", permissionRiderRestrict},
		{http.MethodPost, "/admin/riders/:riderID/restrictions/:restrictionID/lift", permissionRiderRestrict},
		{http.MethodPost, "/admin/riders/:riderID/flags", permissionRiderFlags},
		{http.MethodPost, "/admin/riders/:riderID/flags/:flagID/resolve", permissionRiderFlags},
		{http.MethodGet, "/admin/safety/summary", permissionSafetyRead},
		{http.MethodGet, "/admin/overview", permissionLive},
		{http.MethodGet, "/admin/support/summary", permissionSupportRead},
		{http.MethodGet, "/admin/analytics/summary", permissionTripRead},
		{http.MethodGet, "/admin/promotions/campaigns", permissionPromotionRead},
		{http.MethodPost, "/admin/promotions/campaigns", permissionPromotionWrite},
		{http.MethodPatch, "/admin/communications/campaigns/:campaignID", permissionCommunicationWrite},
		{http.MethodGet, "/admin/communications/campaigns", permissionCommunicationRead},
		{http.MethodPost, "/admin/analytics/summary", ""},
		{http.MethodGet, "/admin/live/drivers", permissionLive},
		{http.MethodGet, "/admin/live/markers", permissionLive},
		{http.MethodPatch, "/admin/safety/incidents/:incidentID", permissionSafetyWrite},
		{http.MethodGet, "/admin/finance/operating-entries", permissionLedgerRead},
		{http.MethodPost, "/admin/trips/:tripID", ""},
		{http.MethodGet, "/admin/unmapped", ""},
	}
	for _, tc := range cases {
		if got := adminRoutePermission(tc.method, tc.path); got != tc.want {
			t.Errorf("%s %s: got %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
	has := func(role, permission string) bool {
		for _, p := range permissionsForRole(role) {
			if p == permission {
				return true
			}
		}
		return false
	}
	if has("operations", permissionFinanceWrite) || has("operations", permissionLedgerRead) || has("operations", permissionSupportContact) || has("finance", permissionSupportContact) || has("safety", permissionDriverContact) || has("support", permissionDriverRead) || has("support", permissionDriverContact) || has("safety", permissionDriverWrite) || has("", permissionTripRead) {
		t.Fatal("role permissions exceed approved ownership")
	}
	if !has("finance", permissionFinanceWrite) || !has("compliance", permissionDriverWrite) || !has("compliance", permissionDriverContact) || !has("support", permissionSupportContact) || !has("super_admin", permissionSettings) {
		t.Fatal("approved owners lack permissions")
	}
}

func TestMultiRolePermissionsAndSuperAdmin(t *testing.T) {
	roles := (adminIdentity{Role: "operations", Roles: []string{"operations", "finance", "safety"}}).effectiveRoles()
	permissions := permissionsForRoles(roles)
	has := func(permission string) bool {
		for _, granted := range permissions {
			if granted == permission {
				return true
			}
		}
		return false
	}
	if !has(permissionGeofenceWrite) || !has(permissionFinanceWrite) || !has(permissionSafetyWrite) || has(permissionDriverWrite) || has(permissionStaff) {
		t.Fatalf("unexpected combined permissions: %v", permissions)
	}
	if got := (adminIdentity{Role: "", Roles: []string{"finance"}}).effectiveRoles(); len(got) != 0 {
		t.Fatalf("unassigned account retained grants: %v", got)
	}
	if got := (adminIdentity{Role: "support", Roles: []string{"finance"}}).effectiveRoles(); len(got) != 1 || got[0] != "support" {
		t.Fatalf("stale secondary grant retained: %v", got)
	}
	if len(permissionsForRole("super_admin")) != len(permissionsForRoles([]string{"operations", "compliance", "support", "finance", "safety", "super_admin"})) {
		t.Fatal("Super Admin does not cover every domain permission")
	}
}

func TestEveryRegisteredAdminRouteHasPermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerAdminRoutes(router, nil, nil)
	for _, route := range router.Routes() {
		if route.Path == "/admin/auth/login" || route.Path == "/admin/auth/accept-invite" || route.Path == "/admin/auth/recovery" || route.Path == "/admin/auth/reset-password" || route.Path == "/admin/auth/verify-email" {
			continue
		}
		if permission := adminRoutePermission(route.Method, route.Path); permission == "" {
			t.Errorf("unmapped staff route: %s %s", route.Method, route.Path)
		}
	}
}
