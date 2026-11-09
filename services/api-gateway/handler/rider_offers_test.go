package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/offers"
	"net/http/httptest"
	"testing"
)

func TestOffersGateAndAdminAuthorization(t *testing.T) {
	t.Setenv("HEYGO_OFFERS_ENABLED", "false")
	r := campaignTestRouter(&adminAPI{}, uuid.NewString())
	base := "/admin/promotions/campaigns/" + uuid.NewString()
	for _, item := range []struct {
		method, path, role string
		csrf               bool
		want               int
	}{
		{"GET", base + "/offer", "support", false, 403},
		{"POST", base + "/activate", "super_admin", false, 403},
		{"POST", base + "/offer/disable", "super_admin", false, 403},
		{"POST", base + "/activate", "super_admin", true, 409},
	} {
		w := campaignTestCall(r, item.method, item.path, nil, item.csrf, item.role)
		if w.Code != item.want {
			t.Fatalf("%s %s: %d %s", item.method, item.path, w.Code, w.Body.String())
		}
	}
	gin.SetMode(gin.TestMode)
	g := gin.New()
	a := &riderOffersAPI{}
	g.GET("/offers", a.list)
	g.POST("/claim", a.claim)
	for path, want := range map[string]int{"/offers": 200, "/claim": 409} {
		w := httptest.NewRecorder()
		method := "GET"
		if path == "/claim" {
			method = "POST"
		}
		g.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		if w.Code != want {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
}

func TestPublicOfferDoesNotExposeCodeOrBudget(t *testing.T) {
	view := publicOffer(offers.Rule{Code: "PRIVATE500", Budget: 50000})
	if _, ok := view["code"]; ok {
		t.Fatal("private code exposed")
	}
	if _, ok := view["budgetKobo"]; ok {
		t.Fatal("internal budget exposed")
	}
	if view["requiresCode"] != true || view["immediateRidesOnly"] != true {
		t.Fatal(view)
	}
}
