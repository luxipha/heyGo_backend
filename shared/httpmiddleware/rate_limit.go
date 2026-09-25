package httpmiddleware

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type visitor struct {
	count int
	reset time.Time
}
type RateLimiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	visitors map[string]visitor
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{limit: limit, window: window, visitors: make(map[string]visitor)}
}

func (l *RateLimiter) Middleware(ctx *gin.Context) {
	if ctx.Request.URL.Path == "/health" || ctx.Request.URL.Path == "/ready" || ctx.Request.URL.Path == "/metrics" {
		ctx.Next()
		return
	}
	now := time.Now()
	key := ctx.ClientIP()
	l.mu.Lock()
	entry := l.visitors[key]
	if entry.reset.Before(now) {
		entry = visitor{reset: now.Add(l.window)}
	}
	entry.count++
	l.visitors[key] = entry
	allowed := entry.count <= l.limit
	l.mu.Unlock()
	if !allowed {
		ctx.Header("Retry-After", strconv.Itoa(int(time.Until(entry.reset).Seconds())+1))
		ctx.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
		return
	}
	ctx.Next()
}
