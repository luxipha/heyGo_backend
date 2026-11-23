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

func TestReferralLookupCodeSurvivesCredentialCleanup(t *testing.T) {
	for _, path := range []string{"/driver/referrals/check", "/rider/referrals/check"} {
		t.Run(path, func(t *testing.T) {
			r := gin.New()
			r.Use(stripCredentialQuery)
			r.GET(path, func(c *gin.Context) {
				if c.Query("code") != "H123456789" {
					t.Error("public referral code lost")
				}
				for _, key := range []string{"access_token", "refresh_token", "token"} {
					if c.Query(key) != "" {
						t.Errorf("credential %s retained", key)
					}
				}
				c.Status(204)
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", path+"?code=H123456789&access_token=secret&refresh_token=secret&token=secret", nil))
			if w.Code != 204 {
				t.Fatal(w.Code)
			}
		})
	}
}
