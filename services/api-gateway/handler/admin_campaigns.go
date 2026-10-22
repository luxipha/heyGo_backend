package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

func registerAdminCampaignRoutes(g *gin.RouterGroup, a *adminAPI) {
	registerAdminPushBroadcastRoutes(g, a)
	for _, kind := range []string{"promotion", "communication"} {
		group := g.Group("/"+kind+"s", func(c *gin.Context) { c.Set("campaignKind", kind); c.Next() })
		group.GET("/campaigns", a.listCampaignDrafts)
		group.GET("/campaigns/:campaignID", a.campaignDraft)
		group.POST("/campaigns", a.requireCSRF, a.createCampaignDraft)
		group.PATCH("/campaigns/:campaignID", a.requireCSRF, a.updateCampaignDraft)
		group.POST("/campaigns/:campaignID/activate", a.requireCSRF, func(c *gin.Context) {
			driverError(c, http.StatusConflict, "campaign_activation_blocked", "Activation requires approved campaign rules and delivery/redemption integration")
		})
	}
}

const campaignJSON = `jsonb_build_object('id',d.id,'kind',d.kind,'name',d.name,'category',d.category,'audience',d.audience,'body',d.body,'budgetKobo',d.budget_kobo,'status',COALESCE((SELECT b.status FROM admin_push_broadcasts b WHERE b.campaign_id=d.id),d.status),'version',d.version,'createdBy',d.created_by,'updatedBy',d.updated_by,'createdAt',d.created_at,'updatedAt',d.updated_at)`

var campaignCapabilities = gin.H{"createDraft": true, "editDraft": true, "activate": false, "send": false, "redeem": false, "reason": "Campaign rules and delivery/redemption integration await approval"}

func campaignID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("campaignID"))
	if err != nil {
		driverError(c, 400, "invalid_campaign_id", "Valid campaign ID is required")
		return uuid.Nil, false
	}
	return id, true
}

func (a *adminAPI) listCampaignDrafts(c *gin.Context) {
	limit := 25
	if raw := c.Query("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 100 {
			driverError(c, 400, "invalid_limit", "Limit must be 1 to 100")
			return
		}
		limit = v
	}
	var cursor any
	if raw := c.Query("cursor"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			driverError(c, 400, "invalid_cursor", "Valid cursor is required")
			return
		}
		cursor = id
	}
	var data json.RawMessage
	err := a.pool.QueryRow(c.Request.Context(), `WITH page AS (SELECT d.* FROM admin_campaign_drafts d WHERE kind=$1 AND ($2::uuid IS NULL OR id>$2) ORDER BY id LIMIT $3), visible AS (SELECT * FROM page ORDER BY id LIMIT $4)
 SELECT jsonb_build_object('items',COALESCE((SELECT jsonb_agg(`+campaignJSON+` ORDER BY d.id) FROM visible d),'[]'::jsonb),
 'nextCursor',CASE WHEN (SELECT COUNT(*) FROM page)>$4 THEN (SELECT id::text FROM visible ORDER BY id DESC LIMIT 1) ELSE NULL END,
 'summary',jsonb_build_object('drafts',(SELECT COUNT(*) FROM admin_campaign_drafts WHERE kind=$1 AND NOT EXISTS(SELECT 1 FROM admin_push_broadcasts b WHERE b.campaign_id=admin_campaign_drafts.id)),'draftBudgetKobo',(SELECT COALESCE(SUM(budget_kobo),0) FROM admin_campaign_drafts WHERE kind=$1),'active',0,'scheduled',(SELECT COUNT(*) FROM admin_push_broadcasts b JOIN admin_campaign_drafts campaign ON campaign.id=b.campaign_id WHERE campaign.kind=$1 AND b.status='scheduled'),'delivered',NULL,'redemptions',NULL),
 'asOf',NOW())`, c.GetString("campaignKind"), cursor, limit+1, limit).Scan(&data)
	if err != nil {
		driverError(c, 503, "campaigns_unavailable", "Campaign drafts are unavailable")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"records": data, "capabilities": func() gin.H {
		if c.GetString("campaignKind") == "communication" {
			return gin.H{"createDraft": true, "editDraft": true, "activate": false, "send": true, "schedule": true, "redeem": false, "reason": "Push broadcasts support send and schedule. Other channels remain draft-only."}
		}
		return campaignCapabilities
	}()}})
}

func (a *adminAPI) campaignDraft(c *gin.Context) {
	id, ok := campaignID(c)
	if !ok {
		return
	}
	var data json.RawMessage
	err := a.pool.QueryRow(c.Request.Context(), `SELECT `+campaignJSON+` FROM admin_campaign_drafts d WHERE d.id=$1 AND d.kind=$2`, id, c.GetString("campaignKind")).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "campaign_not_found", "Campaign draft was not found")
		return
	}
	if err != nil {
		driverError(c, 503, "campaign_unavailable", "Campaign draft is unavailable")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: data})
}

type campaignDraftInput struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Category   string `json:"category"`
	Audience   string `json:"audience"`
	Body       string `json:"body"`
	BudgetKobo int64  `json:"budgetKobo"`
	Version    int    `json:"version"`
	Reason     string `json:"reason"`
}

func (b *campaignDraftInput) valid(kind string) bool {
	b.Name = strings.TrimSpace(b.Name)
	b.Audience = strings.TrimSpace(b.Audience)
	b.Body = strings.TrimSpace(b.Body)
	b.Reason = strings.TrimSpace(b.Reason)
	if utf8.RuneCountInString(b.Name) < 1 || utf8.RuneCountInString(b.Name) > 160 || utf8.RuneCountInString(b.Audience) < 1 || utf8.RuneCountInString(b.Audience) > 240 || utf8.RuneCountInString(b.Body) < 1 || utf8.RuneCountInString(b.Body) > 4000 || utf8.RuneCountInString(b.Reason) < 5 || utf8.RuneCountInString(b.Reason) > 1000 || b.BudgetKobo < 0 || b.BudgetKobo > 1000000000000 {
		return false
	}
	if kind == "promotion" {
		return b.Category == "Discount" || b.Category == "Referral" || b.Category == "Incentive" || b.Category == "Credit"
	}
	return kind == "communication" && b.BudgetKobo == 0 && (b.Category == "Push" || b.Category == "SMS" || b.Category == "WhatsApp" || b.Category == "Email")
}
func decodeCampaignDraft(c *gin.Context) (campaignDraftInput, bool) {
	var b campaignDraftInput
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16384)
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(&b) != nil || dec.Decode(&struct{}{}) != io.EOF || !b.valid(c.GetString("campaignKind")) {
		driverError(c, 400, "invalid_campaign_draft", "Valid draft fields and a reason of 5 to 1000 characters are required")
		return b, false
	}
	return b, true
}
func (a *adminAPI) createCampaignDraft(c *gin.Context) { a.saveCampaignDraft(c, true) }
func (a *adminAPI) updateCampaignDraft(c *gin.Context) { a.saveCampaignDraft(c, false) }
func (a *adminAPI) saveCampaignDraft(c *gin.Context, create bool) {
	b, ok := decodeCampaignDraft(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(b.ID)
	if !create {
		id, ok = campaignID(c)
		if !ok {
			return
		}
		if b.ID != "" && b.ID != id.String() {
			driverError(c, 400, "campaign_id_mismatch", "Campaign IDs must match")
			return
		}
	}
	if create && (err != nil || b.Version != 0) || !create && b.Version < 1 {
		driverError(c, 400, "invalid_campaign_version", "A new UUID is required for creation; updates require the current version")
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		driverError(c, 503, "campaign_unavailable", "Campaign draft could not be saved")
		return
	}
	defer tx.Rollback(c.Request.Context())
	actor := currentAdmin(c).ID
	var data json.RawMessage
	if create {
		err = tx.QueryRow(c.Request.Context(), `INSERT INTO admin_campaign_drafts AS d(id,kind,name,category,audience,body,budget_kobo,created_by,updated_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8::uuid,$8::uuid) ON CONFLICT(id) DO NOTHING RETURNING `+campaignJSON, id, c.GetString("campaignKind"), b.Name, b.Category, b.Audience, b.Body, b.BudgetKobo, actor).Scan(&data)
	} else {
		err = tx.QueryRow(c.Request.Context(), `UPDATE admin_campaign_drafts AS d SET name=$3,category=$4,audience=$5,body=$6,budget_kobo=$7,updated_by=$8::uuid,version=version+1,updated_at=NOW() WHERE id=$1 AND kind=$2 AND version=$9 AND status='draft' AND NOT EXISTS(SELECT 1 FROM admin_push_broadcasts b WHERE b.campaign_id=d.id) RETURNING `+campaignJSON, id, c.GetString("campaignKind"), b.Name, b.Category, b.Audience, b.Body, b.BudgetKobo, actor, b.Version).Scan(&data)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 409, "campaign_conflict", "Draft already exists or changed; reload before saving")
		return
	}
	if err != nil {
		driverError(c, 503, "campaign_unavailable", "Campaign draft could not be saved")
		return
	}
	version := 1
	if !create {
		version = b.Version + 1
	}
	err = insertAdminAudit(c, tx, actor, "campaign.draft_saved", "", "campaign", id.String(), gin.H{"kind": c.GetString("campaignKind"), "reason": b.Reason, "draftOnly": true}, gin.H{"version": b.Version}, gin.H{"version": version, "status": "draft", "kind": c.GetString("campaignKind"), "amountKobo": b.BudgetKobo})
	if err != nil || tx.Commit(c.Request.Context()) != nil {
		driverError(c, 503, "campaign_unavailable", "Campaign draft could not be saved")
		return
	}
	status := 200
	if create {
		status = 201
	}
	c.JSON(status, contracts.APIResponse{Data: data})
}
