package handler

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/luxipha/heyGo_backend/shared/adminauth"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEffectiveSecurityPolicyMatchesEnforcement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	api := &adminAPI{}
	r := gin.New()
	r.GET("/policy", api.securityPolicy)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/policy", nil))
	var body struct {
		Data struct {
			Min      int   `json:"passwordMinBytes"`
			Max      int   `json:"passwordMaxBytes"`
			Lifetime int64 `json:"sessionLifetimeSeconds"`
			Required bool  `json:"twoFactorRequired"`
			Editable bool  `json:"editable"`
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 {
		t.Fatalf("policy failed %d %v", w.Code, err)
	}
	if body.Data.Min != adminauth.MinPasswordBytes || body.Data.Max != adminauth.MaxPasswordBytes || body.Data.Lifetime != int64(adminSessionLifetime/time.Second) || body.Data.Required || body.Data.Editable {
		t.Fatalf("policy differs from enforcement: %+v", body.Data)
	}
	if adminRoutePermission("GET", "/admin/settings/security") != permissionSettings || adminRoutePermission("POST", "/admin/settings/security") != "" {
		t.Fatal("security policy is not settings-scoped and read-only")
	}
}
