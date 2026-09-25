package httpmiddleware

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiterRejectsRequestsPastLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(NewRateLimiter(1, time.Minute).Middleware)
	router.GET("/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/test", nil))
	if first.Code != http.StatusNoContent {
		t.Fatalf("first status=%d", first.Code)
	}
	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/test", nil))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status=%d", second.Code)
	}
}
