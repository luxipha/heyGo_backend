package httpmiddleware

import (
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
	"time"
)

func TestVerifiedIdentityBudgetsAreIndependent(t *testing.T) {
	l := NewRateLimiter(2, time.Minute)
	r := gin.New()
	r.GET("/", func(c *gin.Context) { l.MiddlewareForKey(c, c.GetString("verified")) }, func(c *gin.Context) { c.Status(200) })
	call := func(key string) int {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/", nil)
		l.MiddlewareForKey(c, key)
		return w.Code
	}
	if call("user:a") != 200 || call("user:a") != 200 || call("user:a") != 429 {
		t.Fatal("identity limit missing")
	}
	if call("user:b") != 200 {
		t.Fatal("users share budget")
	}
	if call("ip:shared") != 200 || call("ip:shared") != 200 || call("ip:shared") != 429 {
		t.Fatal("public IP budget missing")
	}
}
