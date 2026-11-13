package httpmiddleware

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRateLimitForwardedHeaders(t *testing.T) {
	for _, cloudRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "cloud-run"}[cloudRun], func(t *testing.T) {
			r := gin.New()
			r.Use(ClientIPMiddleware(cloudRun), NewRateLimiter(1, time.Minute).Middleware)
			r.GET("/", func(c *gin.Context) { c.Status(204) })
			for i, spoof := range []string{"203.0.113.11", "203.0.113.12"} {
				req := httptest.NewRequest("GET", "/", nil)
				req.RemoteAddr = "198.51.100.20:1234"
				xff := spoof
				if cloudRun {
					xff += ", 198.51.100.30"
				}
				req.Header.Set("X-Forwarded-For", xff)
				req.Header.Set("X-Real-IP", spoof)
				req.Header.Set("CF-Connecting-IP", spoof)
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				want := 204
				if i == 1 {
					want = 429
				}
				if w.Code != want {
					t.Fatalf("status %d want %d", w.Code, want)
				}
			}
		})
	}
}

func TestRateLimitDoesNotTrustGinDefaults(t *testing.T) {
	r := gin.New()
	r.Use(NewRateLimiter(1, time.Minute).Middleware)
	r.GET("/", func(c *gin.Context) { c.Status(204) })
	for i, ip := range []string{"203.0.113.11", "203.0.113.12"} {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "198.51.100.20:1234"
		req.Header.Set("X-Forwarded-For", ip)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		want := 204
		if i == 1 {
			want = 429
		}
		if w.Code != want {
			t.Fatal(w.Code)
		}
	}
}

func TestClientIPResolution(t *testing.T) {
	cases := []struct {
		name, header, want string
		cloudRun           bool
	}{
		{"direct ignores spoof", "203.0.113.1", "198.51.100.20", false},
		{"last trusted hop", "203.0.113.1, 2001:db8::1", "2001:db8::1", true},
		{"invalid last hop falls back", "203.0.113.1, invalid", "198.51.100.20", true},
		{"no header", "", "198.51.100.20", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.Use(ClientIPMiddleware(tc.cloudRun))
			r.GET("/", func(c *gin.Context) {
				if got := ClientIP(c); got != tc.want {
					t.Fatalf("got %s want %s", got, tc.want)
				}
			})
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = "198.51.100.20:1234"
			req.Header.Set("X-Forwarded-For", tc.header)
			r.ServeHTTP(httptest.NewRecorder(), req)
		})
	}
}

func TestRateLimiterReclaimsExpiredEntries(t *testing.T) {
	l := NewRateLimiter(1, time.Minute)
	l.visitors["old"] = visitor{count: 1, reset: time.Now().Add(-time.Minute)}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/", nil)
	l.MiddlewareForKey(c, "new")
	if _, ok := l.visitors["old"]; ok {
		t.Fatal("expired entry retained")
	}
}
