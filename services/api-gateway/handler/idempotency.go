package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	gatewayauth "github.com/cprakhar/uber-clone/services/api-gateway/auth"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxIdempotentRequestBytes = 1 << 20

type responseCapture struct {
	gin.ResponseWriter
	body bytes.Buffer
}

func (w *responseCapture) Write(data []byte) (int, error) {
	w.body.Write(data)
	return w.ResponseWriter.Write(data)
}

func (w *responseCapture) WriteString(data string) (int, error) {
	w.body.WriteString(data)
	return w.ResponseWriter.WriteString(data)
}

// idempotencyMiddleware replays completed mutation responses for an actor/key.
// A repeated key with different request content is always rejected.
func idempotencyMiddleware(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		key := strings.TrimSpace(ctx.GetHeader("Idempotency-Key"))
		if key == "" || ctx.Request.Method == http.MethodGet {
			ctx.Next()
			return
		}
		if len(key) > 200 {
			driverError(ctx, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency key is too long")
			ctx.Abort()
			return
		}
		user, ok := gatewayauth.CurrentUser(ctx)
		if !ok {
			driverError(ctx, http.StatusUnauthorized, "authentication_required", "Authentication is required")
			ctx.Abort()
			return
		}
		body, err := io.ReadAll(io.LimitReader(ctx.Request.Body, maxIdempotentRequestBytes+1))
		if err != nil || len(body) > maxIdempotentRequestBytes {
			driverError(ctx, http.StatusRequestEntityTooLarge, "request_too_large", "Request body is too large")
			ctx.Abort()
			return
		}
		ctx.Request.Body = io.NopCloser(bytes.NewReader(body))
		sum := sha256.Sum256(body)
		fingerprint := hex.EncodeToString(sum[:])
		path := ctx.Request.URL.Path
		// Gateway requests time out well before this lease. A crashed request
		// must not leave its key permanently stuck in progress.
		_, err = pool.Exec(ctx.Request.Context(), `DELETE FROM api_idempotency_keys WHERE actor_id=$1::UUID AND key=$2 AND response_status IS NULL AND created_at<NOW()-INTERVAL '2 minutes'`, user.ID, key)
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "idempotency_unavailable", "Idempotency state is unavailable")
			ctx.Abort()
			return
		}
		result, err := pool.Exec(ctx.Request.Context(), `INSERT INTO api_idempotency_keys(actor_id,key,method,path,request_hash)
			VALUES($1::UUID,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, user.ID, key, ctx.Request.Method, path, fingerprint)
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "idempotency_unavailable", "Idempotency state is unavailable")
			ctx.Abort()
			return
		}
		if result.RowsAffected() == 0 {
			var savedMethod, savedPath, savedHash string
			var status *int
			var savedBody []byte
			err := pool.QueryRow(ctx.Request.Context(), `SELECT method,path,request_hash,response_status,response_body FROM api_idempotency_keys WHERE actor_id=$1::UUID AND key=$2`, user.ID, key).Scan(&savedMethod, &savedPath, &savedHash, &status, &savedBody)
			if err != nil {
				driverError(ctx, http.StatusServiceUnavailable, "idempotency_unavailable", "Idempotency state is unavailable")
			} else if savedMethod != ctx.Request.Method || savedPath != path || savedHash != fingerprint {
				driverError(ctx, http.StatusConflict, "idempotency_key_reused", "Idempotency key was used for a different request")
			} else if status == nil {
				driverError(ctx, http.StatusConflict, "request_in_progress", "Request is still in progress")
			} else {
				ctx.Data(*status, "application/json", savedBody)
			}
			ctx.Abort()
			return
		}
		capture := &responseCapture{ResponseWriter: ctx.Writer}
		ctx.Writer = capture
		ctx.Next()
		status := capture.Status()
		if status >= 500 || !json.Valid(capture.body.Bytes()) {
			_, _ = pool.Exec(ctx.Request.Context(), `DELETE FROM api_idempotency_keys WHERE actor_id=$1::UUID AND key=$2 AND response_status IS NULL`, user.ID, key)
			return
		}
		_, _ = pool.Exec(ctx.Request.Context(), `UPDATE api_idempotency_keys SET response_status=$3,response_body=$4::JSONB WHERE actor_id=$1::UUID AND key=$2`, user.ID, key, status, capture.body.Bytes())
	}
}
