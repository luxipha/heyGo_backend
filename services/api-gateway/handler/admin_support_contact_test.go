package handler

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSupportContactRejectsInvalidInputBeforeDatabase(t *testing.T) {
	for _, body := range []string{`{"phone":"1234","reason":"test"}`, `{"phone":"tel:+2348012345678","reason":"test"}`, `{"phone":"+2348012345678","reason":""}`, `{"phone":"+2348012345678","reason":"ab"}`} {
		r := gin.New()
		r.PUT("/support", (&adminAPI{}).putSupportContact)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/support", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("invalid input accepted: %d", w.Code)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if adminRoutePermission(method, "/admin/settings/support-contact") != permissionSettings {
			t.Fatal("settings permission missing")
		}
	}
}
