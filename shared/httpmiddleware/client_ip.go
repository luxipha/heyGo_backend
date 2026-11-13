package httpmiddleware

import (
	"net"
	"strings"

	"github.com/gin-gonic/gin"
)

const rateLimitClientIPKey = "heygo.rate-limit-client-ip"

// ClientIP ignores untrusted forwarding headers and Gin's default trust-all
// proxy configuration. Only server-installed middleware can supply a proxy IP.
func ClientIP(c *gin.Context) string {
	if ip := c.GetString(rateLimitClientIPKey); ip != "" {
		return ip
	}
	if ip := net.ParseIP(c.RemoteIP()); ip != nil {
		return ip.String()
	}
	return "unknown"
}

// ClientIPMiddleware supports the current direct Cloud Run ingress, which
// appends the connecting peer to X-Forwarded-For (one trusted hop). Enable it
// only from the server's K_SERVICE environment, never from request headers.
// Do not extend this to trust arbitrary leftmost or CF-Connecting-IP values.
func ClientIPMiddleware(cloudRun bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := ClientIP(c)
		if cloudRun {
			values := c.Request.Header.Values("X-Forwarded-For")
			if len(values) > 0 {
				hops := strings.Split(values[len(values)-1], ",")
				if peer := net.ParseIP(strings.TrimSpace(hops[len(hops)-1])); peer != nil {
					ip = peer.String()
				}
			}
		}
		c.Set(rateLimitClientIPKey, ip)
		c.Next()
	}
}
