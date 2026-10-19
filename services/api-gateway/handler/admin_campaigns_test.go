package handler

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func campaignTestRouter(api *adminAPI, actor string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/admin", func(c *gin.Context) {
		role := c.GetHeader("Test-Role")
		if role == "" {
			role = "super_admin"
		}
		c.Set("admin", adminIdentity{ID: actor, Role: role})
		c.Set("admin-csrf", "test-csrf")
		c.Next()
	}, api.authorize)
	registerAdminCampaignRoutes(g, api)
	g.GET("/analytics/summary", api.analyticsSummary)
	return r
}
func campaignTestCall(r *gin.Engine, method, path string, body any, csrf bool, role string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if csrf {
		req.Header.Set("X-CSRF-Token", "test-csrf")
	}
	req.Header.Set("Test-Role", role)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}
func TestCampaignAuthorizationAndActivationBlock(t *testing.T) {
	r := campaignTestRouter(&adminAPI{}, uuid.NewString())
	id := uuid.NewString()
	for _, test := range []struct {
		method, path, role string
		csrf               bool
		want               int
	}{
		{"GET", "/admin/promotions/campaigns", "support", false, 403},
		{"GET", "/admin/communications/campaigns", "operations", false, 403},
		{"POST", "/admin/promotions/campaigns", "super_admin", false, 403},
		{"POST", "/admin/communications/campaigns/" + id + "/activate", "super_admin", true, 409},
		{"POST", "/admin/promotions/campaigns/" + id + "/activate", "super_admin", true, 409},
		{"GET", "/admin/analytics/summary?days=365", "operations", false, 400},
		{"GET", "/admin/promotions/campaigns?limit=1000", "super_admin", false, 400},
	} {
		w := campaignTestCall(r, test.method, test.path, nil, test.csrf, test.role)
		if w.Code != test.want {
			t.Errorf("%s %s got %d: %s", test.method, test.path, w.Code, w.Body.String())
		}
	}
	b := map[string]any{"id": id, "name": "Draft", "category": "Discount", "audience": "Riders", "body": "Review proposed benefit", "reason": "Review this draft", "status": "active"}
	if w := campaignTestCall(r, "POST", "/admin/promotions/campaigns", b, true, "super_admin"); w.Code != 400 {
		t.Fatalf("status bypass: %d %s", w.Code, w.Body.String())
	}
}
func TestCampaignDraftValidation(t *testing.T) {
	valid := campaignDraftInput{Name: "Review", Category: "Discount", Audience: "Riders", Body: "Proposed rules", Reason: "Author a draft", BudgetKobo: 100}
	if !valid.valid("promotion") {
		t.Fatal("valid draft rejected")
	}
	invalid := valid
	invalid.BudgetKobo = -1
	if invalid.valid("promotion") {
		t.Fatal("negative budget accepted")
	}
	invalid = valid
	invalid.Reason = "x"
	if invalid.valid("promotion") {
		t.Fatal("reason missing")
	}
	invalid = valid
	invalid.Category = "Email"
	if invalid.valid("promotion") {
		t.Fatal("wrong category accepted")
	}
	invalid = valid
	invalid.Category = "Email"
	invalid.BudgetKobo = 0
	if !invalid.valid("communication") {
		t.Fatal("message draft rejected")
	}
	invalid.BudgetKobo = 1
	if invalid.valid("communication") {
		t.Fatal("message budget accepted")
	}
}
