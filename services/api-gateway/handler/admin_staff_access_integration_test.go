package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/adminauth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestStaffInvitationRecoveryAndSafeAudit(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	t.Setenv("ADMIN_FRONTEND_URL", "https://admin.heygo.example")
	gin.SetMode(gin.TestMode)
	dbctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(dbctx, shareddb.Config{URL: databaseURL, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := shareddb.Migrate(dbctx, pool); err != nil {
		t.Fatal(err)
	}
	superID := uuid.NewString()
	if _, err := pool.Exec(dbctx, `INSERT INTO admin_users(id,username,password_hash,role) VALUES($1::UUID,$2,'unused','super_admin')`, superID, "invite-super-"+superID); err != nil {
		t.Fatal(err)
	}
	var sent []string
	api := &adminAPI{pool: pool, staffEmailSender: func(_ context.Context, _, _, body string) error {
		sent = append(sent, body)
		return nil
	}}
	call := func(method, path, body string, handler gin.HandlerFunc, actor string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(method, path, strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		if actor != "" {
			ctx.Set("admin", adminIdentity{ID: actor, Role: "super_admin"})
		}
		if strings.Contains(path, "/staff/") {
			parts := strings.Split(path, "/")
			if len(parts) > 3 {
				ctx.Params = gin.Params{{Key: "staffID", Value: parts[3]}}
			}
		}
		handler(ctx)
		return rec
	}
	username := "invited-" + uuid.NewString()
	email := username + "@example.com"
	invitedID := ""
	defer func() {
		clean, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = pool.Exec(clean, `DELETE FROM admin_audit WHERE admin_id=$1::UUID OR admin_id=$2::UUID`, superID, uuidOrSuper(invitedID, superID))
		_, _ = pool.Exec(clean, `DELETE FROM admin_users WHERE id=$1::UUID OR id=$2::UUID`, superID, uuidOrSuper(invitedID, superID))
	}()
	invite := call(http.MethodPost, "/admin/staff/invitations", `{"username":"`+username+`","email":"`+email+`","roles":["operations","finance"]}`, api.inviteStaff, superID)
	if invite.Code != http.StatusCreated || len(sent) != 1 {
		t.Fatalf("invite status=%d body=%s sent=%d", invite.Code, invite.Body.String(), len(sent))
	}
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(invite.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	invitedID = created.Data.ID
	var active bool
	if err := pool.QueryRow(dbctx, `SELECT active FROM admin_users WHERE id=$1::UUID`, invitedID).Scan(&active); err != nil || active {
		t.Fatalf("pending staff active=%v err=%v", active, err)
	}
	inviteToken := tokenFromStaffEmail(t, sent[0])
	accept := call(http.MethodPost, "/admin/auth/accept-invite", `{"token":"`+inviteToken+`","password":"a-long-invite-password"}`, api.acceptStaffInvite, "")
	if accept.Code != http.StatusOK {
		t.Fatalf("accept status=%d body=%s", accept.Code, accept.Body.String())
	}
	if replay := call(http.MethodPost, "/admin/auth/accept-invite", `{"token":"`+inviteToken+`","password":"another-long-password"}`, api.acceptStaffInvite, ""); replay.Code != http.StatusBadRequest {
		t.Fatalf("replayed invite status=%d", replay.Code)
	}
	var hash string
	var verified bool
	if err := pool.QueryRow(dbctx, `SELECT active,password_hash,email_verified_at IS NOT NULL FROM admin_users WHERE id=$1::UUID`, invitedID).Scan(&active, &hash, &verified); err != nil || !active || !verified || !adminauth.VerifyPassword(hash, "a-long-invite-password") {
		t.Fatalf("accepted staff active=%v verified=%v hash valid=%v err=%v", active, verified, adminauth.VerifyPassword(hash, "a-long-invite-password"), err)
	}
	oldSession := "old-invited-session-" + uuid.NewString() + uuid.NewString()
	if _, err := pool.Exec(dbctx, `INSERT INTO admin_sessions(token_hash,admin_id,expires_at) VALUES($1,$2::UUID,NOW()+INTERVAL '1 hour')`, adminTokenHash(oldSession), invitedID); err != nil {
		t.Fatal(err)
	}
	unknown := call(http.MethodPost, "/admin/auth/recovery", `{"email":"unknown@example.com"}`, api.requestStaffRecovery, "")
	if unknown.Code != http.StatusAccepted || len(sent) != 1 {
		t.Fatalf("unknown recovery status=%d sent=%d", unknown.Code, len(sent))
	}
	recover := call(http.MethodPost, "/admin/auth/recovery", `{"email":"`+email+`"}`, api.requestStaffRecovery, "")
	if recover.Code != http.StatusAccepted || len(sent) != 2 {
		t.Fatalf("recovery status=%d sent=%d", recover.Code, len(sent))
	}
	recoveryToken := tokenFromStaffEmail(t, sent[1])
	reset := call(http.MethodPost, "/admin/auth/reset-password", `{"token":"`+recoveryToken+`","password":"a-new-recovery-password"}`, api.resetStaffPassword, "")
	if reset.Code != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", reset.Code, reset.Body.String())
	}
	var revoked bool
	if err := pool.QueryRow(dbctx, `SELECT revoked_at IS NOT NULL FROM admin_sessions WHERE token_hash=$1`, adminTokenHash(oldSession)).Scan(&revoked); err != nil || !revoked {
		t.Fatalf("session revoked=%v err=%v", revoked, err)
	}
	if err := pool.QueryRow(dbctx, `SELECT password_hash FROM admin_users WHERE id=$1::UUID`, invitedID).Scan(&hash); err != nil || !adminauth.VerifyPassword(hash, "a-new-recovery-password") {
		t.Fatalf("recovery password valid=%v err=%v", adminauth.VerifyPassword(hash, "a-new-recovery-password"), err)
	}
	newEmail := "verified-" + uuid.NewString() + "@example.com"
	oldEmail := "existing-" + uuid.NewString() + "@example.com"
	if _, err := pool.Exec(dbctx, `UPDATE admin_users SET email=$2,email_verified_at=NOW() WHERE id=$1::UUID`, superID, oldEmail); err != nil {
		t.Fatal(err)
	}
	verifyRequest := call(http.MethodPost, "/admin/staff/"+superID+"/email-verification", `{"email":"`+newEmail+`"}`, api.sendStaffEmailVerification, superID)
	if verifyRequest.Code != http.StatusOK || len(sent) != 3 {
		t.Fatalf("verify request status=%d body=%s sent=%d", verifyRequest.Code, verifyRequest.Body.String(), len(sent))
	}
	var pendingEmail string
	if err := pool.QueryRow(dbctx, `SELECT email FROM admin_users WHERE id=$1::UUID`, superID).Scan(&pendingEmail); err != nil || pendingEmail != oldEmail {
		t.Fatalf("unverified email replaced old recovery address=%q err=%v", pendingEmail, err)
	}
	verifyToken := tokenFromStaffEmail(t, sent[2])
	verify := call(http.MethodPost, "/admin/auth/verify-email", `{"token":"`+verifyToken+`"}`, api.verifyStaffEmail, "")
	if verify.Code != http.StatusOK {
		t.Fatalf("verify status=%d body=%s", verify.Code, verify.Body.String())
	}
	if err := pool.QueryRow(dbctx, `SELECT email FROM admin_users WHERE id=$1::UUID`, superID).Scan(&pendingEmail); err != nil || pendingEmail != newEmail {
		t.Fatalf("verified email=%q err=%v", pendingEmail, err)
	}
	var event staffAuditEvent
	err = pool.QueryRow(dbctx, `SELECT id::TEXT,admin_id::TEXT,action,target_driver_id::TEXT,target_type,target_id,COALESCE(before_state,'{}'::JSONB),COALESCE(after_state,'{}'::JSONB),reason,created_at FROM admin_audit WHERE action='staff.invited' AND target_id=$1`, invitedID).Scan(&event.ID, &event.ActorID, &event.Action, &event.TargetDriverID, &event.TargetType, &event.TargetID, &event.Before, &event.After, &event.Reason, &event.CreatedAt)
	if err != nil || event.TargetType == nil || *event.TargetType != "staff" || strings.Contains(string(event.After), email) {
		t.Fatalf("safe audit target=%v after=%s err=%v", event.TargetType, event.After, err)
	}
	readAudit := httptest.NewRecorder()
	auditCtx, _ := gin.CreateTestContext(readAudit)
	auditCtx.Request = httptest.NewRequest(http.MethodGet, "/admin/audit/"+event.ID, nil)
	auditCtx.Params = gin.Params{{Key: "eventID", Value: event.ID}}
	api.staffAuditDetail(auditCtx)
	if readAudit.Code != http.StatusOK || !strings.Contains(readAudit.Body.String(), `"targetType":"staff"`) || strings.Contains(readAudit.Body.String(), email) {
		t.Fatalf("audit read status=%d body=%s", readAudit.Code, readAudit.Body.String())
	}
}

func tokenFromStaffEmail(t *testing.T, message string) string {
	t.Helper()
	index := strings.Index(message, "#token=")
	if index < 0 {
		t.Fatalf("email lacks token link: %s", message)
	}
	value := strings.Fields(message[index+len("#token="):])[0]
	token, err := url.QueryUnescape(value)
	if err != nil || len(token) < 40 {
		t.Fatalf("invalid link token: %v", err)
	}
	return token
}

func uuidOrSuper(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
