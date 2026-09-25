package handler

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/luxipha/heyGo_backend/shared/adminauth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/env"
	"github.com/luxipha/heyGo_backend/shared/httpmiddleware"
	"github.com/luxipha/heyGo_backend/shared/storage"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const adminCookieName = "heygo_admin_session"
const adminSessionLifetime = 12 * time.Hour

type adminIdentity struct{ ID, Username string }

type adminAPI struct {
	pool         *pgxpool.Pool
	files        storage.ObjectStore
	settingsKey  []byte
	secureCookie bool
	cookieDomain string
	sameSite     http.SameSite
}

func registerAdminRoutes(router *gin.Engine, pool *pgxpool.Pool, files storage.ObjectStore) {
	settingsKey, _ := hex.DecodeString(env.GetString("HEYGO_ADMIN_SETTINGS_KEY", ""))
	if len(settingsKey) != 32 {
		settingsKey = nil
	}
	api := &adminAPI{pool: pool, files: files, settingsKey: settingsKey, secureCookie: env.GetBool("ADMIN_COOKIE_SECURE", true), cookieDomain: env.GetString("ADMIN_COOKIE_DOMAIN", "")}
	api.sameSite = http.SameSiteLaxMode
	if strings.EqualFold(env.GetString("ADMIN_COOKIE_SAME_SITE", "lax"), "none") {
		api.sameSite = http.SameSiteNoneMode
	}
	admin := router.Group("/admin")
	admin.POST("/auth/login", httpmiddleware.NewRateLimiter(5, time.Minute).Middleware, api.login)
	admin.Use(api.authenticate)
	admin.GET("/auth/me", api.me)
	admin.POST("/auth/logout", api.requireCSRF, api.logout)
	registerAdminReviewRoutes(admin, api)
	registerAdminDriverRatingRoutes(admin, api)
	registerAdminGeofenceRoutes(admin, api)
	registerAdminOperatingBalanceRoutes(admin, api)
	registerAdminStatutoryChargeRoutes(admin, api)
	registerNoShowAdminRoutes(admin, api)
	registerAdminPrivacyRoutes(admin, api)
	registerAdminSupportRoutes(admin, pool, files, api.requireCSRF)
}

func adminTokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func adminCSRF(raw string) string {
	mac := hmac.New(sha256.New, []byte(raw))
	mac.Write([]byte("heygo-admin-csrf-v1"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *adminAPI) setCookie(ctx *gin.Context, value string, maxAge int) {
	ctx.SetSameSite(a.sameSite)
	ctx.SetCookie(adminCookieName, value, maxAge, "/admin", a.cookieDomain, a.secureCookie, true)
}

func (a *adminAPI) login(ctx *gin.Context) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if ctx.ShouldBindJSON(&body) != nil || len(body.Username) == 0 || len(body.Username) > 100 || len(body.Password) == 0 || len(body.Password) > 256 {
		driverError(ctx, http.StatusBadRequest, "invalid_credentials", "Username and password are required")
		return
	}
	username := strings.ToLower(strings.TrimSpace(body.Username))
	var id, hash string
	var active bool
	var lockedUntil *time.Time
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT id::TEXT,password_hash,active,locked_until FROM admin_users WHERE username=$1`, username).Scan(&id, &hash, &active, &lockedUntil)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusServiceUnavailable, "admin_auth_unavailable", "Staff sign-in is unavailable")
		return
	}
	valid := err == nil && adminauth.VerifyPassword(hash, body.Password)
	if !valid || !active || (lockedUntil != nil && lockedUntil.After(time.Now())) {
		if err == nil {
			_, _ = a.pool.Exec(ctx.Request.Context(), `UPDATE admin_users SET failed_attempts=failed_attempts+1,
				locked_until=CASE WHEN failed_attempts+1>=5 THEN NOW()+INTERVAL '15 minutes' ELSE locked_until END,
				updated_at=NOW() WHERE id=$1::UUID`, id)
		}
		driverError(ctx, http.StatusUnauthorized, "invalid_credentials", "Invalid username or password")
		return
	}
	_, err = a.pool.Exec(ctx.Request.Context(), `UPDATE admin_users SET failed_attempts=0,locked_until=NULL,updated_at=NOW() WHERE id=$1::UUID`, id)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "admin_auth_unavailable", "Staff sign-in is unavailable")
		return
	}
	bytes := make([]byte, 32)
	if _, err = rand.Read(bytes); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "admin_auth_unavailable", "Staff sign-in is unavailable")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(bytes)
	_, err = a.pool.Exec(ctx.Request.Context(), `INSERT INTO admin_sessions(token_hash,admin_id,expires_at) VALUES($1,$2::UUID,$3)`, adminTokenHash(token), id, time.Now().UTC().Add(adminSessionLifetime))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "admin_auth_unavailable", "Staff sign-in is unavailable")
		return
	}
	a.setCookie(ctx, token, int(adminSessionLifetime.Seconds()))
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id, "username": username, "csrfToken": adminCSRF(token)}})
}

func (a *adminAPI) authenticate(ctx *gin.Context) {
	token, err := ctx.Cookie(adminCookieName)
	if err != nil || len(token) < 40 {
		driverError(ctx, http.StatusUnauthorized, "admin_auth_required", "Staff sign-in is required")
		ctx.Abort()
		return
	}
	var identity adminIdentity
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT u.id::TEXT,u.username FROM admin_sessions s JOIN admin_users u ON u.id=s.admin_id
		WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>NOW() AND u.active=TRUE`, adminTokenHash(token)).Scan(&identity.ID, &identity.Username)
	if err != nil {
		driverError(ctx, http.StatusUnauthorized, "admin_auth_required", "Staff sign-in is required")
		ctx.Abort()
		return
	}
	ctx.Set("admin", identity)
	ctx.Set("admin-csrf", adminCSRF(token))
	ctx.Set("admin-session-token", token)
	ctx.Next()
}

func (a *adminAPI) requireCSRF(ctx *gin.Context) {
	expected, _ := ctx.Get("admin-csrf")
	provided := ctx.GetHeader("X-CSRF-Token")
	if expected == nil || subtle.ConstantTimeCompare([]byte(provided), []byte(expected.(string))) != 1 {
		driverError(ctx, http.StatusForbidden, "csrf_required", "Valid staff CSRF token is required")
		ctx.Abort()
		return
	}
	ctx.Next()
}

func currentAdmin(ctx *gin.Context) adminIdentity {
	value, _ := ctx.Get("admin")
	admin, _ := value.(adminIdentity)
	return admin
}

func (a *adminAPI) me(ctx *gin.Context) {
	admin := currentAdmin(ctx)
	csrf, _ := ctx.Get("admin-csrf")
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": admin.ID, "username": admin.Username, "csrfToken": csrf}})
}

func (a *adminAPI) logout(ctx *gin.Context) {
	token, _ := ctx.Get("admin-session-token")
	_, err := a.pool.Exec(ctx.Request.Context(), `UPDATE admin_sessions SET revoked_at=NOW() WHERE token_hash=$1`, adminTokenHash(token.(string)))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "admin_auth_unavailable", "Staff sign-out is unavailable")
		return
	}
	a.setCookie(ctx, "", -1)
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"signedOut": true}})
}
