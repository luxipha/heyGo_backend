package handler

import (
	"github.com/gin-gonic/gin"
	"strings"
)

// Normalize socket authentication before logging/tracing. Query tokens must
// never become request URL telemetry, even when a client uses the legacy URL form.
func stripCredentialQuery(c *gin.Context) {
	q := c.Request.URL.Query()
	if token := q.Get("access_token"); token != "" && strings.EqualFold(c.GetHeader("Upgrade"), "websocket") && c.GetHeader("Authorization") == "" {
		c.Request.Header.Set("Authorization", "Bearer "+token)
	}
	for _, key := range []string{"access_token", "refresh_token", "token"} {
		q.Del(key)
	}
	// Referral lookup uses a public code, not an authentication credential.
	// Keep it only on the two routes that consume it.
	if c.Request.URL.Path != "/driver/referrals/check" && c.Request.URL.Path != "/rider/referrals/check" {
		q.Del("code")
	}
	c.Request.URL.RawQuery = q.Encode()
	c.Next()
}
