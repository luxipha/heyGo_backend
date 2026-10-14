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
	for _, key := range []string{"access_token", "refresh_token", "token", "code"} {
		q.Del(key)
	}
	c.Request.URL.RawQuery = q.Encode()
	c.Next()
}
