package handler

import (
	"context"
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

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/adminauth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/env"
	"github.com/luxipha/heyGo_backend/shared/httpmiddleware"
	"github.com/luxipha/heyGo_backend/shared/storage"
)

const adminCookieName = "heygo_admin_session"
const adminSessionLifetime = 12 * time.Hour

type adminIdentity struct {
	ID, Username, Role string
	Roles              []string
}

func (a adminIdentity) effectiveRoles() []string {
	if a.Role == "" {
		return []string{}
	}
	roles := make([]string, 0, len(a.Roles)+1)
	seen := make(map[string]bool)
	if a.Role != "" {
		roles = append(roles, a.Role)
		seen[a.Role] = true
	}
	// A direct legacy role change must not retain stale secondary grants.
	if len(a.Roles) > 0 && !hasStaffRole(a.Roles, a.Role) {
		return roles
	}
	for _, role := range a.Roles {
		if role != "" && !seen[role] {
			roles = append(roles, role)
			seen[role] = true
		}
	}
	return roles
}

type adminAPI struct {
	pool             *pgxpool.Pool
	files            storage.ObjectStore
	settingsKey      []byte
	secureCookie     bool
	cookieDomain     string
	sameSite         http.SameSite
	staffEmailSender func(context.Context, string, string, string) error
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
	admin.POST("/auth/accept-invite", httpmiddleware.NewRateLimiter(10, time.Minute).Middleware, api.acceptStaffInvite)
	admin.POST("/auth/recovery", httpmiddleware.NewRateLimiter(5, time.Minute).Middleware, api.requestStaffRecovery)
	admin.POST("/auth/reset-password", httpmiddleware.NewRateLimiter(10, time.Minute).Middleware, api.resetStaffPassword)
	admin.POST("/auth/verify-email", httpmiddleware.NewRateLimiter(10, time.Minute).Middleware, api.verifyStaffEmail)
	admin.Use(api.authenticate)
	admin.Use(api.authorize)
	admin.GET("/settings/security", api.securityPolicy)
	admin.GET("/settings/emergency-contacts", api.getEmergencyDirectory)
	admin.PUT("/settings/emergency-contacts", api.requireCSRF, api.putEmergencyDirectory)
	admin.GET("/settings/support-contact", api.getSupportContact)
	admin.PUT("/settings/support-contact", api.requireCSRF, api.putSupportContact)
	admin.GET("/settings/geo", api.getGeoSettings)
	admin.PUT("/settings/geo", api.requireCSRF, api.putGeoSettings)
	admin.GET("/auth/me", api.me)
	admin.POST("/auth/logout", api.requireCSRF, api.logout)
	registerAdminReviewRoutes(admin, api)
	registerAdminDriverRatingRoutes(admin, api)
	registerAdminGeofenceRoutes(admin, api)
	registerAdminPricingRoutes(admin, api)
	registerAdminOperatingBalanceRoutes(admin, api)
	registerAdminStatutoryChargeRoutes(admin, api)
	registerNoShowAdminRoutes(admin, api)
	registerAdminPrivacyRoutes(admin, api)
	registerAdminSupportRoutes(admin, pool, files, api.requireCSRF)
	registerAdminBugReportRoutes(admin, pool, api.requireCSRF)
	registerAdminTripRiderRoutes(admin, api)
	registerAdminRiderModerationRoutes(admin, api)
	registerAdminAuditRoutes(admin, api)
	registerAdminStaffRoutes(admin, api)
	registerAdminCampaignRoutes(admin, api)
	admin.GET("/analytics/summary", api.analyticsSummary)
	registerAdminLiveRoutes(admin, api)
	registerAdminSafetyRoutes(admin, api)
	registerAdminFinanceReadRoutes(admin, api)
	registerAdminDriverHistoryRoutes(admin, api)
	registerAdminVerificationNoteRoutes(admin, api)
	registerAdminModuleApprovalRoutes(admin, api)
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
	var role *string
	var roles []string
	var active bool
	var lockedUntil *time.Time
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT u.id::TEXT,u.password_hash,u.active,u.locked_until,u.role,
		ARRAY(SELECT r.role FROM admin_user_roles r WHERE r.admin_id=u.id ORDER BY r.role)
		FROM admin_users u WHERE u.username=$1`, username).Scan(&id, &hash, &active, &lockedUntil, &role, &roles)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusServiceUnavailable, "admin_auth_unavailable", "Staff sign-in is unavailable")
		return
	}
	if err == nil && active && lockedUntil != nil && lockedUntil.After(time.Now()) {
		driverError(ctx, http.StatusTooManyRequests, "staff_account_locked", "This staff account is temporarily locked. Try again later")
		return
	}
	valid := err == nil && adminauth.VerifyPassword(hash, body.Password)
	if !valid || !active {
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
	_, err = a.pool.Exec(ctx.Request.Context(), `INSERT INTO admin_audit(id,admin_id,action,target_type,target_id,details,before_state,after_state)
		VALUES($1,$2::UUID,'staff.session.started','staff',$2::TEXT,'{}'::JSONB,'{}'::JSONB,'{}'::JSONB)`, uuid.New(), id)
	if err != nil {
		_, _ = a.pool.Exec(ctx.Request.Context(), `UPDATE admin_sessions SET revoked_at=NOW() WHERE token_hash=$1`, adminTokenHash(token))
		driverError(ctx, http.StatusServiceUnavailable, "admin_auth_unavailable", "Staff sign-in is unavailable")
		return
	}
	a.setCookie(ctx, token, int(adminSessionLifetime.Seconds()))
	identity := adminIdentity{ID: id, Username: username, Role: roleValue(role), Roles: roles}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id, "username": username, "role": role, "roles": identity.effectiveRoles(), "permissions": permissionsForRoles(identity.effectiveRoles()), "csrfToken": adminCSRF(token)}})
}

func (a *adminAPI) authenticate(ctx *gin.Context) {
	token, err := ctx.Cookie(adminCookieName)
	if err != nil || len(token) < 40 {
		driverError(ctx, http.StatusUnauthorized, "admin_auth_required", "Staff sign-in is required")
		ctx.Abort()
		return
	}
	var identity adminIdentity
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT u.id::TEXT,u.username,COALESCE(u.role,''),
		ARRAY(SELECT r.role FROM admin_user_roles r WHERE r.admin_id=u.id ORDER BY r.role)
		FROM admin_sessions s JOIN admin_users u ON u.id=s.admin_id
		WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>NOW() AND u.active=TRUE`, adminTokenHash(token)).Scan(&identity.ID, &identity.Username, &identity.Role, &identity.Roles)
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
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": admin.ID, "username": admin.Username, "role": admin.Role, "roles": admin.effectiveRoles(), "permissions": permissionsForRoles(admin.effectiveRoles()), "csrfToken": csrf}})
}

func (a *adminAPI) logout(ctx *gin.Context) {
	token, _ := ctx.Get("admin-session-token")
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "admin_auth_unavailable", "Staff sign-out is unavailable")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE admin_sessions SET revoked_at=NOW() WHERE token_hash=$1`, adminTokenHash(token.(string)))
	if err == nil {
		err = staffAuditChange(ctx, tx, "staff.session.ended", currentAdmin(ctx).ID, nil, gin.H{})
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "admin_auth_unavailable", "Staff sign-out is unavailable")
		return
	}
	a.setCookie(ctx, "", -1)
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"signedOut": true}})
}
