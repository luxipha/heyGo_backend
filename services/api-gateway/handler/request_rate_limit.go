package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/httpmiddleware"
	"strings"
	"time"
)

// Independent authenticated users behind one NAT must not consume each other's
// general request budget. Authentication still runs on the protected route.
// Invalid/expired/revoked tokens and public requests remain in the IP budget.
func gatewayRequestLimiter(pool *pgxpool.Pool) gin.HandlerFunc {
	limiter := httpmiddleware.NewRateLimiter(120, time.Minute)
	return func(c *gin.Context) {
		key := "ip:" + httpmiddleware.ClientIP(c)
		parts := strings.Fields(c.GetHeader("Authorization"))
		if pool != nil && len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && len(parts[1]) <= 4096 {
			var id string
			e := pool.QueryRow(c.Request.Context(), `SELECT s.user_id::text FROM user_sessions s JOIN users u ON u.id=s.user_id WHERE s.access_token_hash=$1 AND s.revoked_at IS NULL AND s.access_expires_at>NOW() AND u.active`, gatewayauth.TokenHash(parts[1])).Scan(&id)
			if e == nil {
				key = "user:" + id
			}
		}
		limiter.MiddlewareForKey(c, key)
	}
}
