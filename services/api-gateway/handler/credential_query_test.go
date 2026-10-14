package handler

import (
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCredentialQueryIsRemovedBeforeTelemetryAndSocketAuthPreserved(t *testing.T) {
	r := gin.New()
	r.Use(stripCredentialQuery)
	r.GET("/ws", func(c *gin.Context) {
		if strings.Contains(c.Request.URL.String(), "secret") {
			t.Error("query credentials retained")
		}
		if c.GetHeader("Authorization") != "Bearer secret-access" {
			t.Error("socket auth lost")
		}
		if c.Query("afterEventId") != "evt_1" {
			t.Error("event cursor lost")
		}
		c.Status(204)
	})
	req := httptest.NewRequest("GET", "/ws?access_token=secret-access&refresh_token=secret-refresh&token=secret-token&code=secret-code&afterEventId=evt_1", nil)
	req.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
}
