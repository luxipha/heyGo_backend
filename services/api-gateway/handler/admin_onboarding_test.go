package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestOnboardingRoleIsReadOnlyAndIsolated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if !validStaffRoles([]string{"onboarding"}) {
		t.Fatal("onboarding role cannot be invited")
	}
	permissions := permissionsForRole("onboarding")
	if len(permissions) != 1 || permissions[0] != permissionOnboardingRead {
		t.Fatalf("excessive grants: %v", permissions)
	}
	cases := []struct {
		method, path string
		want         int
	}{
		{"GET", "/admin/onboarding/drivers", 204},
		{"GET", "/admin/onboarding/summary", 204},
		{"GET", "/admin/onboarding/drivers/:driverID", 204},
		{"POST", "/admin/onboarding/drivers", 403},
		{"GET", "/admin/drivers", 403},
		{"GET", "/admin/drivers/:driverID/documents/:documentID/files/:side", 403},
		{"POST", "/admin/drivers/:driverID/approval", 403},
		{"GET", "/admin/finance/operating-entries", 403},
		{"GET", "/admin/staff", 403},
		{"GET", "/admin/trips", 403},
	}
	for _, tc := range cases {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			r := gin.New()
			a := &adminAPI{}
			r.Handle(tc.method, tc.path, func(c *gin.Context) { c.Set("admin", adminIdentity{ID: "caller", Role: "onboarding"}); c.Next() }, a.authorize, func(c *gin.Context) { c.Status(http.StatusNoContent) })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.want {
				t.Fatalf("got %d want %d", w.Code, tc.want)
			}
		})
	}
}
