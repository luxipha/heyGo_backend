package auth

import (
	"errors"
	"net/http"
	"strings"

	sharedauth "github.com/luxipha/heyGo_backend/shared/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/gin-gonic/gin"
)

const userContextKey = "authenticated-user"

type Middleware struct {
	verifier sharedauth.Verifier
	users    UserStore
}

func NewMiddleware(verifier sharedauth.Verifier, users UserStore) *Middleware {
	return &Middleware{verifier: verifier, users: users}
}

func (m *Middleware) Authenticate(ctx *gin.Context) {
	rawToken := bearerToken(ctx.GetHeader("Authorization"))
	if rawToken == "" && strings.EqualFold(ctx.GetHeader("Upgrade"), "websocket") {
		rawToken = ctx.Query("access_token")
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
