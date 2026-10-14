package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

type staffAccount struct {
	ID            string    `json:"id"`
	Username      string    `json:"username"`
	Email         *string   `json:"email,omitempty"`
	EmailVerified bool      `json:"emailVerified"`
	Role          *string   `json:"role"`
	Roles         []string  `json:"roles"`
	Active        bool      `json:"active"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func registerAdminStaffRoutes(group *gin.RouterGroup, a *adminAPI) {
	group.GET("/staff", a.listStaff)
	group.GET("/staff/:staffID", a.staffDetail)
	group.PATCH("/staff/:staffID", a.requireCSRF, a.updateStaff)
	group.POST("/staff/invitations", a.requireCSRF, a.inviteStaff)
	group.POST("/staff/:staffID/invite", a.requireCSRF, a.resendStaffInvite)
	group.POST("/staff/:staffID/email-verification", a.requireCSRF, a.sendStaffEmailVerification)
}

func validStaffRole(role string) bool {
	_, ok := rolePermissions[role]
	return ok
}

func scanStaffAccount(row pgx.Row) (staffAccount, error) {
	var item staffAccount
	err := row.Scan(&item.ID, &item.Username, &item.Email, &item.EmailVerified, &item.Role, &item.Roles, &item.Active, &item.CreatedAt, &item.UpdatedAt)
	if err == nil {
		item.Roles = (adminIdentity{Role: roleValue(item.Role), Roles: item.Roles}).effectiveRoles()
	}
	return item, err
}

const staffSelect = `SELECT u.id::TEXT,u.username,u.email,u.email_verified_at IS NOT NULL,u.role,
	ARRAY(SELECT r.role FROM admin_user_roles r WHERE r.admin_id=u.id ORDER BY r.role),
	u.active,u.created_at,u.updated_at FROM admin_users u`

func hasStaffRole(roles []string, role string) bool {
	for _, item := range roles {
		if item == role {
			return true
		}
	}
	return false
}

func (a *adminAPI) listStaff(ctx *gin.Context) {
	limit, at, id, ok := staffPage(ctx)
	if !ok {
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), staffSelect+` WHERE ($1::TIMESTAMPTZ IS NULL OR (u.created_at,u.id)<($1,$2::UUID)) ORDER BY u.created_at DESC,u.id DESC LIMIT $3`, at, id, limit+1)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_unavailable", "Staff accounts are unavailable")
		return
	}
	defer rows.Close()
	items := make([]staffAccount, 0, limit+1)
	for rows.Next() {
		item, err := scanStaffAccount(rows)
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "staff_unavailable", "Staff accounts are unavailable")
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_unavailable", "Staff accounts are unavailable")
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		encoded := encodeDriverTripHistoryCursor(driverTripHistoryCursor{OccurredAt: items[len(items)-1].CreatedAt, ID: items[len(items)-1].ID})
		next = &encoded
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items, Meta: contracts.APIMeta{NextCursor: next}})
}

func (a *adminAPI) staffDetail(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("staffID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_staff_id", "Valid staff ID is required")
		return
	}
	item, err := scanStaffAccount(a.pool.QueryRow(ctx.Request.Context(), staffSelect+` WHERE u.id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "staff_not_found", "Staff account was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_unavailable", "Staff account is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: item})
}

func (a *adminAPI) updateStaff(ctx *gin.Context) {
	id, err := uuid.Parse(ctx.Param("staffID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_staff_id", "Valid staff ID is required")
		return
	}
	if id.String() == currentAdmin(ctx).ID {
		driverError(ctx, http.StatusConflict, "self_change_forbidden", "A different Super Admin must change this account")
		return
	}
	var body struct {
		Role   *string   `json:"role"`
		Roles  *[]string `json:"roles"`
		Active *bool     `json:"active"`
		Reason string    `json:"reason"`
	}
	if ctx.ShouldBindJSON(&body) != nil || (body.Role == nil && body.Roles == nil && body.Active == nil) || (body.Role != nil && body.Roles != nil) || (body.Role != nil && !validStaffRole(*body.Role)) {
		driverError(ctx, http.StatusBadRequest, "invalid_staff_change", "Valid roles or active status are required")
		return
	}
	if body.Roles != nil {
		seen := make(map[string]bool)
		for _, role := range *body.Roles {
			if !validStaffRole(role) || seen[role] {
				driverError(ctx, http.StatusBadRequest, "invalid_staff_change", "Roles must be unique and valid")
				return
			}
			seen[role] = true
		}
	}
	if body.Roles != nil && len(*body.Roles) == 0 && (body.Active == nil || *body.Active) {
		driverError(ctx, http.StatusBadRequest, "invalid_staff_change", "Deactivate an account before removing all roles")
		return
	}
	body.Reason = strings.TrimSpace(body.Reason)
	if len(body.Reason) < 5 || len(body.Reason) > 500 {
		driverError(ctx, http.StatusBadRequest, "staff_reason_required", "A 5 to 500 character reason is required")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_unavailable", "Staff change is unavailable")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	// Serialize changes to the Super Admin set, including concurrent demotions.
	if _, err = tx.Exec(ctx.Request.Context(), `SELECT pg_advisory_xact_lock(hashtext('heygo_admin_roles'))`); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_unavailable", "Staff change is unavailable")
		return
	}
	old, err := scanStaffAccount(tx.QueryRow(ctx.Request.Context(), staffSelect+` WHERE u.id=$1 FOR UPDATE OF u`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "staff_not_found", "Staff account was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_unavailable", "Staff change is unavailable")
		return
	}
	nextRoles := old.Roles
	if body.Role != nil {
		nextRoles = []string{*body.Role}
	}
	if body.Roles != nil {
		nextRoles = *body.Roles
	}
	var nextRole *string
	if len(nextRoles) > 0 {
		nextRole = &nextRoles[0]
	}
	nextActive := old.Active
	if body.Active != nil {
		nextActive = *body.Active
	}
	if old.Active && hasStaffRole(old.Roles, "super_admin") && (!nextActive || !hasStaffRole(nextRoles, "super_admin")) {
		var others int
		if err = tx.QueryRow(ctx.Request.Context(), `SELECT COUNT(*) FROM admin_users u WHERE u.id<>$1 AND u.active=TRUE AND
			(u.role='super_admin' OR EXISTS (SELECT 1 FROM admin_user_roles r WHERE r.admin_id=u.id AND r.role='super_admin'))`, id).Scan(&others); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "staff_unavailable", "Staff change is unavailable")
			return
		}
		if others == 0 {
			driverError(ctx, http.StatusConflict, "last_super_admin", "At least one active Super Admin is required")
			return
		}
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE admin_users SET role=$2,active=$3,updated_at=NOW() WHERE id=$1`, id, nextRole, nextActive)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_unavailable", "Staff change is unavailable")
		return
	}
	if body.Role != nil || body.Roles != nil {
		if _, err = tx.Exec(ctx.Request.Context(), `DELETE FROM admin_user_roles WHERE admin_id=$1`, id); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "staff_unavailable", "Staff change is unavailable")
			return
		}
		for _, role := range nextRoles {
			if _, err = tx.Exec(ctx.Request.Context(), `INSERT INTO admin_user_roles(admin_id,role,granted_by) VALUES($1,$2,$3::UUID)`, id, role, currentAdmin(ctx).ID); err != nil {
				driverError(ctx, http.StatusServiceUnavailable, "staff_unavailable", "Staff change is unavailable")
				return
			}
		}
	}
	item, err := scanStaffAccount(tx.QueryRow(ctx.Request.Context(), staffSelect+` WHERE u.id=$1`, id))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_unavailable", "Staff change is unavailable")
		return
	}
	if _, err = tx.Exec(ctx.Request.Context(), `UPDATE admin_sessions SET revoked_at=NOW() WHERE admin_id=$1 AND revoked_at IS NULL`, id); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_unavailable", "Staff change is unavailable")
		return
	}
	err = staffAuditChangeWithReason(ctx, tx, "staff.account.updated", id.String(), body.Reason, gin.H{"roles": old.Roles, "active": old.Active}, gin.H{"roles": item.Roles, "active": item.Active})
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_unavailable", "Staff change is unavailable")
		return
	}
	if err = tx.Commit(ctx.Request.Context()); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "staff_unavailable", "Staff change is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: item})
}
