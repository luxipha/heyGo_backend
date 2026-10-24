package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"net/http"
)

func registerSessionActivityRoutes(g *gin.RouterGroup, pool *pgxpool.Pool) {
	g.GET("/auth/sessions", func(c *gin.Context) {
		u, _ := gatewayauth.CurrentUser(c)
		hash := gatewayauth.TokenHash(bearerFromHeader(c.GetHeader("Authorization")))
		var data []byte
		err := pool.QueryRow(c.Request.Context(), `SELECT jsonb_build_object('items',COALESCE((SELECT jsonb_agg(s ORDER BY s."createdAt" DESC) FROM (SELECT id::text,device_name AS "deviceName",created_at AS "createdAt",last_used_at AS "lastUsedAt",refresh_expires_at AS "expiresAt",revoked_at AS "revokedAt",access_token_hash=$2 AS current,revoked_at IS NULL AND refresh_expires_at>NOW() AS active FROM user_sessions WHERE user_id=$1::uuid ORDER BY created_at DESC LIMIT 50)s),'[]'::jsonb))`, u.ID, hash).Scan(&data)
		if err != nil {
			driverError(c, 503, "sessions_unavailable", "Sessions could not be loaded")
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Data(200, "application/json", append(append([]byte(`{"data":`), data...), '}'))
	})
	revoke := func(c *gin.Context, others bool) {
		u, _ := gatewayauth.CurrentUser(c)
		hash := gatewayauth.TokenHash(bearerFromHeader(c.GetHeader("Authorization")))
		id := c.Param("sessionID")
		if !others {
			if _, err := uuid.Parse(id); err != nil {
				driverError(c, 400, "invalid_session", "Invalid session")
				return
			}
		}
		tx, err := pool.Begin(c.Request.Context())
		if err != nil {
			reviewFailure(c)
			return
		}
		defer tx.Rollback(c.Request.Context())
		var current string
		if err = tx.QueryRow(c.Request.Context(), `SELECT id::text FROM user_sessions WHERE user_id=$1::uuid AND access_token_hash=$2 AND revoked_at IS NULL FOR UPDATE`, u.ID, hash).Scan(&current); err != nil {
			driverError(c, 409, "session_unavailable", "This login does not support session management")
			return
		}
		if !others {
			var owned bool
			if err = tx.QueryRow(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM user_sessions WHERE user_id=$1::uuid AND id=$2::uuid)`, u.ID, id).Scan(&owned); err != nil {
				reviewFailure(c)
				return
			}
			if !owned {
				driverError(c, 404, "session_not_found", "Session was not found")
				return
			}
		}
		_, err = tx.Exec(c.Request.Context(), `UPDATE user_sessions SET revoked_at=COALESCE(revoked_at,NOW()) WHERE user_id=$1::uuid AND (($3 AND id<>$2::uuid) OR (NOT $3 AND id=$4::uuid))`, u.ID, current, others, func() any {
			if others {
				return nil
			}
			return id
		}())
		if err == nil {
			_, err = tx.Exec(c.Request.Context(), `DELETE FROM driver_devices WHERE driver_id=$1::uuid AND session_id IN (SELECT id FROM user_sessions WHERE user_id=$1::uuid AND revoked_at IS NOT NULL)`, u.ID)
		}
		if err == nil {
			_, err = tx.Exec(c.Request.Context(), `DELETE FROM driver_socket_sessions WHERE driver_id=$1::uuid AND auth_session_id IN (SELECT id FROM user_sessions WHERE user_id=$1::uuid AND revoked_at IS NOT NULL)`, u.ID)
		}
		if err != nil || tx.Commit(c.Request.Context()) != nil {
			reviewFailure(c)
			return
		}
		c.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"revoked": true, "current": !others && id == current}})
	}
	g.DELETE("/auth/sessions/:sessionID", func(c *gin.Context) { revoke(c, false) })
	g.POST("/auth/sessions/revoke-others", func(c *gin.Context) { revoke(c, true) })
}
