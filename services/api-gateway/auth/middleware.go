package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	sharedauth "github.com/luxipha/heyGo_backend/shared/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

const userContextKey = "authenticated-user"

type Middleware struct {
	verifier sharedauth.Verifier
	users    UserStore
	pool     *pgxpool.Pool
	store    *PostgresUserStore
}

func NewMiddleware(verifier sharedauth.Verifier, users UserStore) *Middleware {
	return &Middleware{verifier: verifier, users: users}
}

func NewSessionMiddleware(pool *pgxpool.Pool, users *PostgresUserStore) *Middleware {
	return &Middleware{pool: pool, store: users}
}

func (m *Middleware) Authenticate(ctx *gin.Context) {
	rawToken := bearerToken(ctx.GetHeader("Authorization"))
	if rawToken == "" && strings.EqualFold(ctx.GetHeader("Upgrade"), "websocket") {
		rawToken = ctx.Query("access_token")
	}
	if m.pool != nil && m.store != nil {
		m.authenticateSession(ctx, rawToken)
		return
	}
	identity, err := m.verifier.Verify(ctx.Request.Context(), rawToken)
	if err != nil {
		ctx.AbortWithStatusJSON(http.StatusUnauthorized, contracts.APIErrorResponse{Error: contracts.APIError{Code: "authentication_required", Message: "Valid CasperID authentication is required"}})
		return
	}
	user, err := m.users.UpsertIdentity(ctx.Request.Context(), identity)
	if err != nil {
		if errors.Is(err, ErrAccountDeactivated) {
			ctx.AbortWithStatusJSON(http.StatusForbidden, contracts.APIErrorResponse{Error: contracts.APIError{Code: "account_deactivated", Message: "This HeyGo account is deactivated"}})
			return
		}
		ctx.AbortWithStatusJSON(http.StatusServiceUnavailable, contracts.APIErrorResponse{Error: contracts.APIError{Code: "identity_mapping_unavailable", Message: "Identity mapping is unavailable"}})
		return
	}
	ctx.Set(userContextKey, user)
	ctx.Next()
}

func (m *Middleware) authenticateSession(ctx *gin.Context, rawToken string) {
	if rawToken == "" {
		ctx.AbortWithStatusJSON(http.StatusUnauthorized, contracts.APIErrorResponse{Error: contracts.APIError{Code: "authentication_required", Message: "Valid HeyGo authentication is required"}})
		return
	}
	hash := TokenHash(rawToken)
	var userID, sessionID string
	err := m.pool.QueryRow(ctx.Request.Context(), `UPDATE user_sessions SET last_used_at=NOW()
		WHERE access_token_hash=$1 AND revoked_at IS NULL AND access_expires_at>NOW()
		RETURNING user_id::TEXT,id::TEXT`, hash).Scan(&userID, &sessionID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			ctx.AbortWithStatusJSON(http.StatusServiceUnavailable, contracts.APIErrorResponse{Error: contracts.APIError{Code: "authentication_unavailable", Message: "Authentication is temporarily unavailable"}})
			return
		}
		ctx.AbortWithStatusJSON(http.StatusUnauthorized, contracts.APIErrorResponse{Error: contracts.APIError{Code: "authentication_required", Message: "Valid HeyGo authentication is required"}})
		return
	}
	user, err := m.store.ByID(ctx.Request.Context(), userID)
	if err != nil {
		if errors.Is(err, ErrAccountDeactivated) {
			ctx.AbortWithStatusJSON(http.StatusForbidden, contracts.APIErrorResponse{Error: contracts.APIError{Code: "account_deactivated", Message: "This HeyGo account is deactivated"}})
			return
		}
		ctx.AbortWithStatusJSON(http.StatusServiceUnavailable, contracts.APIErrorResponse{Error: contracts.APIError{Code: "authentication_unavailable", Message: "Authentication is temporarily unavailable"}})
		return
	}
	ctx.Set("authenticated-session-id", sessionID)
	ctx.Set(userContextKey, user)
	ctx.Next()
}

func TokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

const AccessTokenLifetime = 15 * time.Minute
const RefreshTokenLifetime = 30 * 24 * time.Hour

func RequireRole(role string) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		user, ok := CurrentUser(ctx)
		if !ok || !user.HasRole(role) {
			ctx.AbortWithStatusJSON(http.StatusForbidden, contracts.APIErrorResponse{Error: contracts.APIError{Code: "role_required", Message: role + " role is required"}})
			return
		}
		ctx.Next()
	}
}

func CurrentUser(ctx *gin.Context) (User, bool) {
	value, exists := ctx.Get(userContextKey)
	user, ok := value.(User)
	return user, exists && ok
}

func bearerToken(header string) string {
	parts := strings.SplitN(strings.TrimSpace(header), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// CurrentSessionID identifies the authenticated session independently of token rotation.
func CurrentSessionID(ctx *gin.Context) string {
	value, _ := ctx.Get("authenticated-session-id")
	id, _ := value.(string)
	return id
}
