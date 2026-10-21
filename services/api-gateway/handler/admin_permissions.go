package handler

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	permissionDriverRead         = "drivers.read"
	permissionDriverContact      = "drivers.contact.read"
	permissionDriverWrite        = "drivers.review"
	permissionGeofenceRead       = "geofences.read"
	permissionGeofenceWrite      = "geofences.manage"
	permissionFinanceRead        = "finance.read"
	permissionFinanceWrite       = "finance.manage"
	permissionSupportRead        = "support.read"
	permissionSupportContact     = "support.case.contact.read"
	permissionSupportWrite       = "support.manage"
	permissionSafetyRead         = "safety.read"
	permissionSafetyWrite        = "safety.review"
	permissionTripRead           = "trips.read"
	permissionRiderRead          = "riders.read"
	permissionRiderRestrict      = "riders.restrict"
	permissionRiderFlags         = "riders.flags.write"
	permissionSettings           = "settings.manage"
	permissionStaff              = "staff.manage"
	permissionAudit              = "audit.read"
	permissionLive               = "live.read"
	permissionLedgerRead         = "finance.ledger.read"
	permissionPromotionRead      = "promotions.read"
	permissionPromotionWrite     = "promotions.manage"
	permissionCommunicationRead  = "communications.read"
	permissionCommunicationWrite = "communications.manage"
)

var rolePermissions = map[string][]string{
	"operations":  {permissionGeofenceRead, permissionGeofenceWrite, permissionFinanceRead, permissionTripRead, permissionRiderRead, permissionLive},
	"compliance":  {permissionDriverRead, permissionDriverContact, permissionDriverWrite, permissionTripRead, permissionRiderRead},
	"support":     {permissionSupportRead, permissionSupportContact, permissionSupportWrite, permissionTripRead, permissionRiderRead, permissionRiderFlags},
	"finance":     {permissionFinanceRead, permissionFinanceWrite, permissionLedgerRead, permissionTripRead, permissionRiderRead},
	"safety":      {permissionSafetyRead, permissionSafetyWrite, permissionTripRead, permissionRiderRead, permissionRiderRestrict, permissionRiderFlags, permissionLive},
	"super_admin": {permissionPromotionRead, permissionPromotionWrite, permissionCommunicationRead, permissionCommunicationWrite, permissionDriverRead, permissionDriverContact, permissionDriverWrite, permissionLedgerRead, permissionGeofenceRead, permissionGeofenceWrite, permissionFinanceRead, permissionFinanceWrite, permissionSupportRead, permissionSupportContact, permissionSupportWrite, permissionSafetyRead, permissionSafetyWrite, permissionTripRead, permissionRiderRead, permissionRiderRestrict, permissionRiderFlags, permissionSettings, permissionStaff, permissionAudit, permissionLive},
}

func roleValue(role *string) string {
	if role == nil {
		return ""
	}
	return *role
}

func permissionsForRole(role string) []string {
	return permissionsForRoles([]string{role})
}

func permissionsForRoles(roles []string) []string {
	seen := make(map[string]bool)
	permissions := make([]string, 0)
	for _, role := range roles {
		for _, permission := range rolePermissions[role] {
			if !seen[permission] {
				seen[permission] = true
				permissions = append(permissions, permission)
			}
		}
	}
	sort.Strings(permissions)
	return permissions
}

func adminRoutePermission(method, path string) string {
	if path == "/admin/auth/me" || path == "/admin/auth/logout" {
		return "authenticated"
	}
	read := method == http.MethodGet
	switch {
	case path == "/admin/drivers" || strings.HasPrefix(path, "/admin/drivers/") || strings.HasPrefix(path, "/admin/account-deletion-requests"):
		if read {
			return permissionDriverRead
		}
		return permissionDriverWrite
	case strings.HasPrefix(path, "/admin/driver-ratings"):
		if read {
			return permissionSafetyRead
		}
		return permissionSafetyWrite
	case strings.HasPrefix(path, "/admin/geofences"):
		if read {
			return permissionGeofenceRead
		}
		return permissionGeofenceWrite
	case strings.HasPrefix(path, "/admin/operating-balance") || strings.HasPrefix(path, "/admin/statutory-charge-rules") || strings.HasPrefix(path, "/admin/no-show") || strings.HasPrefix(path, "/admin/pricing"):
		if read {
			return permissionFinanceRead
		}
		return permissionFinanceWrite
	case strings.HasPrefix(path, "/admin/support"):
		if path == "/admin/support/cases/:caseID/contact" && read {
			return permissionSupportContact
		}
		if read {
			return permissionSupportRead
		}
		return permissionSupportWrite
	case path == "/admin/settings/security":
		if read {
			return permissionSettings
		}
	case strings.HasPrefix(path, "/admin/settings/support-contact"), strings.HasPrefix(path, "/admin/settings/review-otp"), strings.HasPrefix(path, "/admin/settings/geo"), strings.HasPrefix(path, "/admin/settings/resend"), strings.HasPrefix(path, "/admin/settings/email-delivery"), strings.HasPrefix(path, "/admin/settings/sms"):
		return permissionSettings
	case strings.HasPrefix(path, "/admin/promotions/"):
		if read {
			return permissionPromotionRead
		}
		return permissionPromotionWrite
	case strings.HasPrefix(path, "/admin/communications/"):
		if read {
			return permissionCommunicationRead
		}
		return permissionCommunicationWrite
	case path == "/admin/analytics/summary":
		if read {
			return permissionTripRead
		}
	case path == "/admin/trips" || strings.HasPrefix(path, "/admin/trips/"):
		if read {
			return permissionTripRead
		}
	case path == "/admin/riders" || strings.HasPrefix(path, "/admin/riders/"):
		if strings.Contains(path, "/restrictions") {
			if read {
				return permissionRiderRead
			}
			return permissionRiderRestrict
		}
		if strings.Contains(path, "/flags") {
			if read {
				return permissionRiderRead
			}
			return permissionRiderFlags
		}
		if read {
			return permissionRiderRead
		}
	case path == "/admin/staff" || strings.HasPrefix(path, "/admin/staff/"):
		return permissionStaff
	case path == "/admin/audit" || strings.HasPrefix(path, "/admin/audit/"):
		if read {
			return permissionAudit
		}
	case path == "/admin/live/drivers" || path == "/admin/live/markers" || path == "/admin/overview":
		if read {
			return permissionLive
		}
	case path == "/admin/safety/summary" || path == "/admin/safety/incidents" || path == "/admin/safety/sos-alerts" || strings.HasPrefix(path, "/admin/safety/incidents/"):
		if read {
			return permissionSafetyRead
		}
		return permissionSafetyWrite
	case strings.HasPrefix(path, "/admin/finance/"):
		if read {
			return permissionLedgerRead
		}
		return permissionFinanceWrite
	}
	return ""
}

func (a *adminAPI) authorize(ctx *gin.Context) {
	needed := adminRoutePermission(ctx.Request.Method, ctx.FullPath())
	if needed == "authenticated" {
		ctx.Next()
		return
	}
	if needed != "" && adminHasPermission(ctx, needed) {
		ctx.Next()
		return
	}
	driverError(ctx, http.StatusForbidden, "admin_permission_denied", "Staff permission is required")
	ctx.Abort()
}

func adminHasPermission(ctx *gin.Context, permission string) bool {
	for _, granted := range permissionsForRoles(currentAdmin(ctx).effectiveRoles()) {
		if granted == permission {
			return true
		}
	}
	return false
}
