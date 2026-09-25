package logs

import (
	"strings"
	"time"

	"github.com/luxipha/heyGo_backend/shared/observe/correlation"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// HTTPLoggingMiddleware returns a handler that logs structured request/response data.
// It captures status code, response size, latency, request method, path, remote IP,
// scheme, and an optional request ID (X-Request-ID header). Health endpoints are
// downgraded to debug level to reduce noise.
func HTTPLoggingMiddleware(c *gin.Context) {
	r := c.Request
	start := time.Now()
	l := L()

	// Pre-handler data
	method := r.Method
	path := r.URL.Path
	route := c.FullPath() // matched route pattern, may be "" for 404
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	reqID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if reqID == "" || len(reqID) > 128 {
		reqID = uuid.NewString()
	}
	r = r.WithContext(correlation.WithID(r.Context(), reqID))
	c.Request = r
	c.Header("X-Correlation-ID", reqID)

	remoteIP := c.ClientIP()
	ua := r.UserAgent()
	referer := r.Referer()

	// Let downstream handlers run (including recovery middleware if registered)
	c.Next()

	latency := time.Since(start)
	status := c.Writer.Status()
	size := c.Writer.Size()

	requestFields := map[string]interface{}{
		"method":    method,
		"path":      path,
		"route":     route,
		"scheme":    scheme,
		"remoteIP":  remoteIP,
		"requestID": reqID,
		"userAgent": ua,
		"referer":   referer,
	}

	responseFields := map[string]interface{}{
		"status":    status,
		"bytes":     size,
		"latencyMs": latency.Milliseconds(),
	}

	switch {
	case path == "/health" || path == "/ready":
		l.Debugw("http request", "request", requestFields, "response", responseFields)
	case status >= 500:
		l.Errorw("http request", "request", requestFields, "response", responseFields)
	case status >= 400:
		l.Warnw("http request", "request", requestFields, "response", responseFields)
	default:
		l.Infow("http request", "request", requestFields, "response", responseFields)
	}
}
