package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

// Every query fragment below is a server-owned constant, never request input.
// Queues live beside the owning module and inherit its route permission.
type moduleApprovalConfig struct {
	path, table, state, proposer, until, summary string
	retireTable, retireFK                        string
}

var moduleApprovalConfigs = []moduleApprovalConfig{
	{"/geofences", "trip_geofences", "status", "created_by", "effective_until", "jsonb_build_object('kind',p.kind,'code',p.code,'version',p.version)", "trip_geofence_retirements", "geofence_id"},
	{"/pricing", "pricing_versions", "status", "created_by", "effective_until", "jsonb_build_object('marketCode',p.market_code,'version',p.version)", "pricing_retirements", "pricing_id"},
	{"/operating-balance/policies", "operating_balance_policies", "status", "created_by", "effective_until", "jsonb_build_object('marketCode',p.market_code,'version',p.version,'minimumKobo',p.minimum_kobo,'warningKobo',p.warning_kobo)", "operating_balance_policy_retirements", "policy_id"},
	{"/statutory-charge-rules", "statutory_charge_rules", "status", "created_by", "effective_until", "jsonb_build_object('ledgerCode',p.ledger_code,'version',p.version,'name',p.name)", "statutory_charge_rule_retirements", "rule_id"},
	{"/no-show/policies", "no_show_policies", "status", "created_by", "effective_until", "jsonb_build_object('marketCode',p.market_code,'version',p.version,'waitMinutes',p.wait_minutes,'feeKobo',p.fee_kobo)", "no_show_policy_retirements", "policy_id"},
	{"/finance/refund-policy", "refund_policy_versions", "state", "proposed_by", "effective_to", "jsonb_build_object('version',p.version,'maxRequestKobo',p.max_request_kobo,'maxDailyKobo',p.max_daily_kobo)", "refund_policy_retirements", "policy_id"},
}

func registerAdminModuleApprovalRoutes(g *gin.RouterGroup, a *adminAPI) {
	for _, config := range moduleApprovalConfigs {
		cfg := config
		g.GET(cfg.path+"/pending-approvals", func(c *gin.Context) { a.pendingModuleApprovals(c, cfg) })
		g.GET(cfg.path+"/:id/decision-receipt", func(c *gin.Context) { a.moduleDecisionReceipt(c, cfg) })
		if cfg.retireTable != "" {
			g.GET(cfg.path+"/:id/retirement-requests/:requestID/decision-receipt", func(c *gin.Context) { a.moduleRetirementReceipt(c, cfg) })
		}
	}
}

func moduleCanApprove(c *gin.Context, cfg moduleApprovalConfig) bool {
	if cfg.path == "/geofences" {
		return adminHasPermission(c, permissionGeofenceWrite)
	}
	return adminHasPermission(c, permissionFinanceWrite)
}

func (a *adminAPI) pendingModuleApprovals(c *gin.Context, cfg moduleApprovalConfig) {
	query := fmt.Sprintf(`SELECT p.id::TEXT,p.id::TEXT,'draft',p.%s::TEXT,NULL::TEXT,p.created_at,p.effective_from,p.%s,%s
		FROM %s p WHERE p.%s='draft' ORDER BY p.created_at DESC LIMIT 101`, cfg.proposer, cfg.until, cfg.summary, cfg.table, cfg.state)
	rows, err := a.pool.Query(c.Request.Context(), query)
	if err != nil {
		reviewFailure(c)
		return
	}
	items, err := scanModuleApprovalRows(rows, cfg.path, currentAdmin(c).ID, moduleCanApprove(c, cfg))
	if err != nil {
		reviewFailure(c)
		return
	}
	if cfg.retireTable != "" {
		query = fmt.Sprintf(`SELECT r.id::TEXT,p.id::TEXT,'retirement',r.requested_by::TEXT,NULL::TEXT,r.created_at,p.effective_from,r.effective_until,%s
			FROM %s r JOIN %s p ON p.id=r.%s WHERE r.status='pending' ORDER BY r.created_at DESC LIMIT 101`, cfg.summary, cfg.retireTable, cfg.table, cfg.retireFK)
		rows, err = a.pool.Query(c.Request.Context(), query)
		if err != nil {
			reviewFailure(c)
			return
		}
		more, scanErr := scanModuleApprovalRows(rows, cfg.path, currentAdmin(c).ID, moduleCanApprove(c, cfg))
		if scanErr != nil {
			reviewFailure(c)
			return
		}
		items = append(items, more...)
	}
	if len(items) > 100 {
		driverError(c, 409, "approval_queue_limit", "The module queue exceeds 100 items; resolve older drafts first")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: items})
}

func scanModuleApprovalRows(rows pgx.Rows, path, actor string, canApprove bool) ([]gin.H, error) {
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var id, parent, kind, proposer string
		var approver *string
		var created, from time.Time
		var until *time.Time
		var summary []byte
		if err := rows.Scan(&id, &parent, &kind, &proposer, &approver, &created, &from, &until, &summary); err != nil {
			return nil, err
		}
		destination := "/admin" + path + "/" + parent
		if kind == "retirement" {
			destination += "/retirement-requests/" + id
		}
		items = append(items, gin.H{"id": id, "targetId": parent, "kind": kind, "status": "pending", "proposedBy": proposer, "approvedBy": approver, "createdAt": created, "effectiveFrom": from, "effectiveUntil": until, "summary": json.RawMessage(summary), "destination": destination, "mayApprove": canApprove && proposer != actor})
	}
	return items, rows.Err()
}

func (a *adminAPI) moduleDecisionReceipt(c *gin.Context, cfg moduleApprovalConfig) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		driverError(c, 400, "invalid_approval_id", "Valid approval ID is required")
		return
	}
	query := fmt.Sprintf(`SELECT p.%s,p.%s::TEXT,p.approved_by::TEXT,p.created_at,p.approved_at,p.effective_from,p.%s,%s FROM %s p WHERE p.id=$1`, cfg.state, cfg.proposer, cfg.until, cfg.summary, cfg.table)
	var state, proposer string
	var approver *string
	var created, from time.Time
	var approvedAt, until *time.Time
	var summary []byte
	err = a.pool.QueryRow(c.Request.Context(), query, id).Scan(&state, &proposer, &approver, &created, &approvedAt, &from, &until, &summary)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "approval_not_found", "Approval was not found")
		return
	}
	if err != nil {
		reviewFailure(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"id": id.String(), "kind": "draft", "status": state, "proposedBy": proposer, "approvedBy": approver, "proposedAt": created, "decidedAt": approvedAt, "effectiveFrom": from, "effectiveUntil": until, "summary": json.RawMessage(summary), "mayApprove": moduleCanApprove(c, cfg) && state == "draft" && proposer != currentAdmin(c).ID}})
}

func (a *adminAPI) moduleRetirementReceipt(c *gin.Context, cfg moduleApprovalConfig) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		driverError(c, 400, "invalid_approval_id", "Valid approval ID is required")
		return
	}
	requestID, err := uuid.Parse(c.Param("requestID"))
	if err != nil {
		driverError(c, 400, "invalid_retirement_id", "Valid retirement ID is required")
		return
	}
	query := fmt.Sprintf(`SELECT r.status,r.requested_by::TEXT,r.approved_by::TEXT,r.created_at,r.approved_at,r.effective_until,%s FROM %s r JOIN %s p ON p.id=r.%s WHERE r.id=$1 AND p.id=$2`, cfg.summary, cfg.retireTable, cfg.table, cfg.retireFK)
	var state, proposer string
	var approver *string
	var created, until time.Time
	var approvedAt *time.Time
	var summary []byte
	err = a.pool.QueryRow(c.Request.Context(), query, requestID, id).Scan(&state, &proposer, &approver, &created, &approvedAt, &until, &summary)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "retirement_not_found", "Retirement request was not found")
		return
	}
	if err != nil {
		reviewFailure(c)
		return
	}
	c.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": requestID.String(), "targetId": id.String(), "kind": "retirement", "status": state, "proposedBy": proposer, "approvedBy": approver, "proposedAt": created, "decidedAt": approvedAt, "effectiveUntil": until, "summary": json.RawMessage(summary), "mayApprove": moduleCanApprove(c, cfg) && state == "pending" && proposer != currentAdmin(c).ID}})
}
