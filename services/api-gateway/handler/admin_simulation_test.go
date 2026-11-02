package handler

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSimulationPermissions(t *testing.T) {
	for _, path := range []string{"/admin/simulation/runs", "/admin/simulation/runs/:runID", "/admin/simulation/runs/:runID/participants"} {
		if adminRoutePermission("GET", path) != permissionLive {
			t.Fatal("read permission mismatch")
		}
		if adminRoutePermission("POST", path) != permissionSimulationManage {
			t.Fatal("write permission mismatch")
		}
	}
	for _, role := range []string{"operations", "safety", "finance", "support", "compliance"} {
		for _, permission := range permissionsForRole(role) {
			if permission == permissionSimulationManage {
				t.Fatal("existing role unexpectedly gained simulation mutation")
			}
		}
	}
	found := false
	for _, permission := range permissionsForRole("super_admin") {
		found = found || permission == permissionSimulationManage
	}
	if !found {
		t.Fatal("super admin missing simulation permission")
	}
}
func TestSimulationBodyRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	for _, body := range []string{`{"name":"test","seed":1,"start":true}`, `{"name":"test","seed":1} {}`, `{"name":"test","seed":1,"accessToken":"not-allowed"}`} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		var b struct {
			Name string `json:"name"`
			Seed int64  `json:"seed"`
		}
		if simulationBody(c, &b) {
			t.Fatal("unexpected body accepted")
		}
	}
}
