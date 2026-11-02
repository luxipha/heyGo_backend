package handler

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/luxipha/heyGo_backend/shared/geo"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminRoutingRejectsMissingCredentialBeforeSaving(t *testing.T) {
	t.Setenv("GOOGLE_ROUTES_API_KEY", "")
	t.Setenv("AMAP_ROUTES_API_KEY", "")
	for _, provider := range []string{"google", "amap"} {
		settings := geo.Settings{RouteProvider: provider, MapProvider: "google", PlaceProvider: "google", NavigationProvider: "google"}
		body, _ := json.Marshal(map[string]any{"settings": settings, "version": 0, "reason": "Select routing provider"})
		router := gin.New()
		api := &adminAPI{}
		router.PUT("/geo", api.putGeoSettings)
		req := httptest.NewRequest(http.MethodPut, "/geo", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != 400 || !strings.Contains(response.Body.String(), "routing_credential_missing") {
			t.Fatalf("missing credential accepted: %d %s", response.Code, response.Body.String())
		}
	}
}
