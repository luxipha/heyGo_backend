package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/luxipha/heyGo_backend/shared/adminauth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

const staffInviteLifetime = 24 * time.Hour
const staffRecoveryLifetime = 15 * time.Minute

func validStaffEmail(raw string) (string, bool) {
	email := strings.ToLower(strings.TrimSpace(raw))
	parsed, err := mail.ParseAddress(email)
	return email, err == nil && parsed.Address == email && parsed.Name == "" && len(email) <= 254
}

func validStaffRoles(roles []string) bool {
	if len(roles) == 0 || len(roles) > len(rolePermissions) {
		return false
	}
	seen := make(map[string]bool)
	for _, role := range roles {
		if !validStaffRole(role) || seen[role] {
			return false
		}
		seen[role] = true
	}
	return true
}

func staffFrontendBase() (string, bool) {
	raw := strings.TrimSpace(os.Getenv("ADMIN_FRONTEND_URL"))
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", false
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")) {
		return "", false
	}
	return strings.TrimSuffix(u.String(), "/"), true
}

func newStaffAccessToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (a *adminAPI) sendStaffEmail(ctx context.Context, to, subject, body string) error {
	if a.staffEmailSender != nil {
		return a.staffEmailSender(ctx, to, subject, body)
	}
	return sendConfiguredEmail(ctx, a.pool, a.settingsKey, to, subject, body, "")
}

func staffAccessLink(base, purpose, token string) string {
	path := "/staff/accept-invite"
	if purpose == "recovery" {
		path = "/staff/reset-password"
	} else if purpose == "verify_email" {
		path = "/staff/verify-email"
	}
	// The fragment is read by the Admin client and is not sent in HTTP requests.
	return base + path + "#token=" + url.QueryEscape(token)
}

func (a *adminAPI) deliverStaffToken(ctx *gin.Context, adminID, email, purpose, token string) bool {
	base, ok := staffFrontendBase()
	if !ok {
		driverError(ctx, http.StatusServiceUnavailable, "staff_invitation_unavailable", "Staff access URL is not configured")
		return false
	}
	subject := "HeyGo staff invitation"
	if purpose == "recovery" {
		subject = "Reset your HeyGo staff password"
	} else if purpose == "verify_email" {
		subject = "Verify your HeyGo staff email"
	}
	message := subject + "\n\nOpen this one-time link: " + staffAccessLink(base, purpose, token) + "\n\nIf you did not request this, ignore this message."
	if err := a.sendStaffEmail(ctx.Request.Context(), email, subject, message); err != nil {
		_, _ = a.pool.Exec(ctx.Request.Context(), `DELETE FROM admin_access_tokens WHERE token_hash=$1 AND admin_id=$2::UUID`, adminTokenHash(token), adminID)
		driverError(ctx, http.StatusServiceUnavailable, "staff_email_unavailable", "Staff email could not be sent")
		return false
	}
	return true
}

func (a *adminAPI) inviteStaff(ctx *gin.Context) {
	var body struct {
		Username string   `json:"username"`
		Email    string   `json:"email"`
		Roles    []string `json:"roles"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_staff_invitation", "Username, email, and roles are required")
		return
	}
	username := strings.ToLower(strings.TrimSpace(body.Username))
	email, validEmail := validStaffEmail(body.Email)
	if len(username) < 3 || len(username) > 100 || strings.ContainsAny(username, " \t\r\n") || !validEmail || !validStaffRoles(body.Roles) {
		driverError(ctx, http.StatusBadRequest, "invalid_staff_invitation", "Valid username, email, and roles are required")
		return
	}
	if _, ok := staffFrontendBase(); !ok {
		driverError(ctx, http.StatusServiceUnavailable, "staff_invitation_unavailable", "Staff access URL is not configured")
		return
	}
	token, err := newStaffAccessToken()
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_invitation_unavailable", "Staff invitation is unavailable")
		return
	}
	placeholder, err := newStaffAccessToken()
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_invitation_unavailable", "Staff invitation is unavailable")
		return
	}
	hash, err := adminauth.HashPassword(placeholder)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_invitation_unavailable", "Staff invitation is unavailable")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_invitation_unavailable", "Staff invitation is unavailable")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	id := uuid.NewString()
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO admin_users(id,username,email,password_hash,active,role) VALUES($1::UUID,$2,$3,$4,FALSE,$5)`, id, username, email, hash, body.Roles[0])
	if err != nil {
		driverError(ctx, http.StatusConflict, "staff_exists", "A staff account with that username or email already exists")
		return
	}
	for _, role := range body.Roles {
		if _, err = tx.Exec(ctx.Request.Context(), `INSERT INTO admin_user_roles(admin_id,role,granted_by) VALUES($1::UUID,$2,$3::UUID)`, id, role, currentAdmin(ctx).ID); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "staff_invitation_unavailable", "Staff invitation is unavailable")
			return
		}
	}
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO admin_access_tokens(token_hash,admin_id,email,purpose,expires_at) VALUES($1,$2::UUID,$3,'invite',$4)`, adminTokenHash(token), id, email, time.Now().UTC().Add(staffInviteLifetime))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_invitation_unavailable", "Staff invitation is unavailable")
		return
	}
	if err = staffAuditChange(ctx, tx, "staff.invited", id, nil, gin.H{"username": username, "roles": body.Roles, "active": false}); err != nil || tx.Commit(ctx.Request.Context()) != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_invitation_unavailable", "Staff invitation is unavailable")
		return
	}
	if !a.deliverStaffToken(ctx, id, email, "invite", token) {
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": id, "username": username, "email": email, "roles": body.Roles, "active": false, "inviteExpiresAt": time.Now().UTC().Add(staffInviteLifetime)}})
}

func (a *adminAPI) resendStaffInvite(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("staffID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_staff_id", "Valid staff ID is required")
		return
	}
	if _, ok := staffFrontendBase(); !ok {
		driverError(ctx, http.StatusServiceUnavailable, "staff_invitation_unavailable", "Staff access URL is not configured")
		return
	}
	token, err := newStaffAccessToken()
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_invitation_unavailable", "Staff invitation is unavailable")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_invitation_unavailable", "Staff invitation is unavailable")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var email string
	var active bool
	var verified *time.Time
	err = tx.QueryRow(ctx.Request.Context(), `SELECT email,active,email_verified_at FROM admin_users WHERE id=$1 AND email IS NOT NULL FOR UPDATE`, id).Scan(&email, &active, &verified)
	if errors.Is(err, pgx.ErrNoRows) || active || verified != nil {
		driverError(ctx, http.StatusConflict, "staff_not_pending", "Only a pending staff invitation can be resent")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_invitation_unavailable", "Staff invitation is unavailable")
		return
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE admin_access_tokens SET consumed_at=NOW() WHERE admin_id=$1 AND purpose='invite' AND consumed_at IS NULL`, id)
	if err == nil {
		_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO admin_access_tokens(token_hash,admin_id,email,purpose,expires_at) VALUES($1,$2,$3,'invite',$4)`, adminTokenHash(token), id, email, time.Now().UTC().Add(staffInviteLifetime))
	}
	if err == nil {
		err = staffAuditChange(ctx, tx, "staff.invitation.resent", id.String(), nil, gin.H{"invitationPending": true})
	}
	if err != nil || tx.Commit(ctx.Request.Context()) != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_invitation_unavailable", "Staff invitation is unavailable")
		return
	}
	if !a.deliverStaffToken(ctx, id.String(), email, "invite", token) {
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"sent": true}})
}

func (a *adminAPI) acceptStaffInvite(ctx *gin.Context) {
	a.consumeStaffAccessToken(ctx, "invite")
}

func (a *adminAPI) resetStaffPassword(ctx *gin.Context) {
	a.consumeStaffAccessToken(ctx, "recovery")
}

func (a *adminAPI) verifyStaffEmail(ctx *gin.Context) {
	a.consumeStaffAccessToken(ctx, "verify_email")
}

func (a *adminAPI) consumeStaffAccessToken(ctx *gin.Context, purpose string) {
	var body struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if ctx.ShouldBindJSON(&body) != nil || len(body.Token) < 40 || len(body.Token) > 128 {
		driverError(ctx, http.StatusBadRequest, "invalid_staff_token", "Valid token is required")
		return
	}
	var passwordHash string
	if purpose != "verify_email" {
		var err error
		passwordHash, err = adminauth.HashPassword(body.Password)
		if err != nil {
			driverError(ctx, http.StatusBadRequest, "weak_staff_password", "Password must be 12 to 256 bytes")
			return
		}
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_access_unavailable", "Staff access is unavailable")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var id, email string
	err = tx.QueryRow(ctx.Request.Context(), `SELECT t.admin_id::TEXT,t.email FROM admin_access_tokens t JOIN admin_users u ON u.id=t.admin_id
		WHERE t.token_hash=$1 AND t.purpose=$2 AND t.consumed_at IS NULL AND t.expires_at>NOW()
		AND (t.purpose='verify_email' OR u.email=t.email)
		FOR UPDATE OF t,u`, adminTokenHash(body.Token), purpose).Scan(&id, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusBadRequest, "staff_token_expired", "The link is invalid or expired")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_access_unavailable", "Staff access is unavailable")
		return
	}
	var tag pgconn.CommandTag
	switch purpose {
	case "invite":
		tag, err = tx.Exec(ctx.Request.Context(), `UPDATE admin_users SET password_hash=$2,active=TRUE,email_verified_at=NOW(),failed_attempts=0,locked_until=NULL,updated_at=NOW() WHERE id=$1::UUID AND active=FALSE`, id, passwordHash)
	case "recovery":
		tag, err = tx.Exec(ctx.Request.Context(), `UPDATE admin_users SET password_hash=$2,failed_attempts=0,locked_until=NULL,updated_at=NOW() WHERE id=$1::UUID AND active=TRUE AND email_verified_at IS NOT NULL`, id, passwordHash)
	case "verify_email":
		tag, err = tx.Exec(ctx.Request.Context(), `UPDATE admin_users SET email=$2,email_verified_at=NOW(),updated_at=NOW() WHERE id=$1::UUID AND active=TRUE`, id, email)
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_access_unavailable", "Staff access is unavailable")
		return
	}
	if tag.RowsAffected() != 1 {
		driverError(ctx, http.StatusBadRequest, "staff_token_expired", "The link is invalid or expired")
		return
	}
	if _, err = tx.Exec(ctx.Request.Context(), `UPDATE admin_sessions SET revoked_at=NOW() WHERE admin_id=$1::UUID AND revoked_at IS NULL`, id); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_access_unavailable", "Staff access is unavailable")
		return
	}
	if purpose == "verify_email" {
		if _, err = tx.Exec(ctx.Request.Context(), `UPDATE admin_access_tokens SET consumed_at=NOW() WHERE admin_id=$1::UUID AND purpose='recovery' AND consumed_at IS NULL`, id); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "staff_access_unavailable", "Staff access is unavailable")
			return
		}
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE admin_access_tokens SET consumed_at=NOW() WHERE admin_id=$1::UUID AND purpose=$2 AND consumed_at IS NULL`, id, purpose)
	if err != nil || staffAuditChangeForActor(ctx, tx, id, "staff."+purpose+".completed", id, nil, gin.H{"emailVerified": true}) != nil || tx.Commit(ctx.Request.Context()) != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_access_unavailable", "Staff access is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"completed": true, "email": email}})
}

func (a *adminAPI) requestStaffRecovery(ctx *gin.Context) {
	var body struct {
		Email string `json:"email"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_staff_email", "Valid email is required")
		return
	}
	email, ok := validStaffEmail(body.Email)
	if !ok {
		driverError(ctx, http.StatusBadRequest, "invalid_staff_email", "Valid email is required")
		return
	}
	// The same response is returned for unknown, inactive, and unverified accounts.
	accepted := func() { ctx.JSON(http.StatusAccepted, contracts.APIResponse{Data: gin.H{"accepted": true}}) }
	if _, ok := staffFrontendBase(); !ok {
		driverError(ctx, http.StatusServiceUnavailable, "staff_access_unavailable", "Staff access is unavailable")
		return
	}
	var id string
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT id::TEXT FROM admin_users WHERE email=$1 AND active=TRUE AND email_verified_at IS NOT NULL`, email).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		accepted()
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_access_unavailable", "Staff access is unavailable")
		return
	}
	token, err := newStaffAccessToken()
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_access_unavailable", "Staff access is unavailable")
		return
	}
	_, err = a.pool.Exec(ctx.Request.Context(), `INSERT INTO admin_access_tokens(token_hash,admin_id,email,purpose,expires_at) VALUES($1,$2::UUID,$3,'recovery',$4)`, adminTokenHash(token), id, email, time.Now().UTC().Add(staffRecoveryLifetime))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_access_unavailable", "Staff access is unavailable")
		return
	}
	base, _ := staffFrontendBase()
	message := "Reset your HeyGo staff password\n\nOpen this one-time link: " + staffAccessLink(base, "recovery", token) + "\n\nIf you did not request this, ignore this message."
	if err = a.sendStaffEmail(ctx.Request.Context(), email, "Reset your HeyGo staff password", message); err != nil {
		_, _ = a.pool.Exec(ctx.Request.Context(), `DELETE FROM admin_access_tokens WHERE token_hash=$1`, adminTokenHash(token))
	}
	accepted()
}

func (a *adminAPI) sendStaffEmailVerification(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("staffID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_staff_id", "Valid staff ID is required")
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_staff_email", "Valid email is required")
		return
	}
	email, ok := validStaffEmail(body.Email)
	if !ok {
		driverError(ctx, http.StatusBadRequest, "invalid_staff_email", "Valid email is required")
		return
	}
	if _, ok := staffFrontendBase(); !ok {
		driverError(ctx, http.StatusServiceUnavailable, "staff_access_unavailable", "Staff access URL is not configured")
		return
	}
	token, err := newStaffAccessToken()
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_access_unavailable", "Staff email verification is unavailable")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_access_unavailable", "Staff email verification is unavailable")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var active bool
	err = tx.QueryRow(ctx.Request.Context(), `SELECT active FROM admin_users WHERE id=$1 FOR UPDATE`, id).Scan(&active)
	if err != nil || !active {
		driverError(ctx, http.StatusConflict, "staff_email_unavailable", "Active staff account is required")
		return
	}
	var emailTaken bool
	err = tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM admin_users WHERE email=$1 AND id<>$2)`, email, id).Scan(&emailTaken)
	if err != nil || emailTaken {
		driverError(ctx, http.StatusConflict, "staff_email_unavailable", "Email is already assigned to another staff account")
		return
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE admin_access_tokens SET consumed_at=NOW() WHERE admin_id=$1 AND purpose='verify_email' AND consumed_at IS NULL`, id)
	if err == nil {
		_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO admin_access_tokens(token_hash,admin_id,email,purpose,expires_at) VALUES($1,$2,$3,'verify_email',$4)`, adminTokenHash(token), id, email, time.Now().UTC().Add(staffInviteLifetime))
	}
	if err == nil {
		err = staffAuditChange(ctx, tx, "staff.email.verification_sent", id.String(), nil, gin.H{"emailVerificationPending": true})
	}
	if err != nil || tx.Commit(ctx.Request.Context()) != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_access_unavailable", "Staff email verification is unavailable")
		return
	}
	if !a.deliverStaffToken(ctx, id.String(), email, "verify_email", token) {
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"sent": true}})
}
