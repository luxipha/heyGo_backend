package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "heygo_http_requests_total", Help: "HTTP requests processed."}, []string{"service", "method", "route", "status"})
	httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "heygo_http_request_duration_seconds", Help: "HTTP request latency.", Buckets: prometheus.DefBuckets}, []string{"service", "method", "route"})
)

func init() { prometheus.MustRegister(httpRequests, httpDuration) }

func HTTPMiddleware(service string) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		httpRequests.WithLabelValues(service, c.Request.Method, route, strconv.Itoa(c.Writer.Status())).Inc()
		httpDuration.WithLabelValues(service, c.Request.Method, route).Observe(time.Since(start).Seconds())
	}
}
func Handler() http.Handler { return promhttp.Handler() }
