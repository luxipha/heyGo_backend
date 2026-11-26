package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/referrals"
	"strconv"
	"strings"
)

func (a *adminAPI) heroStages(c *gin.Context) {
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 || offset > 100000 {
		driverError(c, 400, "invalid_offset", "Invalid page")
		return
	}
	view := c.DefaultQuery("view", "all")
	if view != "all" && view != "current" {
		driverError(c, 400, "invalid_view", "Invalid stage view")
		return
	}
	items, err := referrals.ListStagePage(c.Request.Context(), a.pool, c.Query("heroId"), offset, view)
	if err != nil {
		referralError(c, err)
		return
	}
	more := len(items) > 200
	if more {
		items = items[:200]
	}
	heroData(c, gin.H{"items": items, "hasMore": more})
}
func (a *adminAPI) approveHeroStages(c *gin.Context) {
	var b struct {
		IDs []string `json:"ids"`
	}
	if c.ShouldBindJSON(&b) != nil || len(b.IDs) < 1 || len(b.IDs) > 100 {
		driverError(c, 400, "invalid_stages", "Select up to 100 stages")
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		referralError(c, err)
		return
	}
	defer tx.Rollback(ctx)
	if err = referrals.ApproveStages(ctx, tx, b.IDs, currentAdmin(c).ID); err != nil {
		driverError(c, 409, "stage_not_approvable", "A stage is not qualified. Reload and check its status.")
		return
	}
	if err = adminAudit(c, tx, "referrals.stages.approved", "", gin.H{"ids": b.IDs}); err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		referralError(c, err)
		return
	}
	heroData(c, gin.H{"approved": len(b.IDs)})
}

func (a *heroesAPI) stages(c *gin.Context) {
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 || offset > 100000 {
		driverError(c, 400, "invalid_offset", "Invalid page")
		return
	}
	items, err := referrals.ListStagePage(c.Request.Context(), a.pool, heroID(c), offset, "public")
	if err != nil {
		referralError(c, err)
		return
	}
	// Finance metadata and the Hero's identity are not needed on the public client.
	for i := range items {
		items[i].ApprovedBy = nil
		items[i].HeroID = ""
		items[i].Hero = ""
	}
	more := len(items) > 200
	if more {
		items = items[:200]
	}
	heroData(c, gin.H{"items": items, "hasMore": more})
}

func (a *adminAPI) queueHeroPayout(c *gin.Context) {
	var b struct {
		StageID string `json:"stageId"`
	}
	if c.ShouldBindJSON(&b) != nil {
		driverError(c, 400, "invalid_stage", "Select a stage")
		return
	}
	if _, err := uuid.Parse(b.StageID); err != nil {
		driverError(c, 400, "invalid_stage", "Select a valid stage")
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		referralError(c, err)
		return
	}
	defer tx.Rollback(ctx)
	staff := currentAdmin(c).ID
	id, err := referrals.QueueStagePayout(ctx, tx, b.StageID, &staff)
	if err != nil {
		driverError(c, 409, "stage_not_payable", "Stage is paid, uncredited, or needs a verified Hero bank account and enabled Monnify settings.")
		return
	}
	if err = adminAudit(c, tx, "heroes.payout.queued", "", gin.H{"payoutId": id, "stageId": b.StageID}); err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		referralError(c, err)
		return
	}
	heroData(c, gin.H{"id": id, "queued": true})
}
func (a *adminAPI) authorizeHeroPayout(c *gin.Context) {
	var b struct {
		Code string `json:"authorizationCode"`
	}
	if c.ShouldBindJSON(&b) != nil || len(b.Code) < 4 || len(b.Code) > 10 || strings.Trim(b.Code, "0123456789") != "" {
		driverError(c, 400, "invalid_authorization", "Enter the Monnify authorization code")
		return
	}
	id := c.Param("id")
	var response heroTransferResponse
	if err := paymentInternalRequest(c.Request.Context(), "/internal/hero-transfers", map[string]string{"id": id, "action": "authorize", "authorizationCode": b.Code}, &response); err != nil {
		driverError(c, 409, "authorization_unconfirmed", "Authorization is unconfirmed. Check the code and refresh the payout status.")
		return
	}
	if err := a.auditHeroPayoutAction(c, "heroes.payout.authorization.submitted", id); err != nil {
		heroFailure(c)
		return
	}
	// Status reconciliation independently verifies destination and amount.
	heroData(c, gin.H{"submitted": true})
}
func (a *heroesAPI) banks(c *gin.Context) {
	var response struct {
		Data []map[string]string `json:"data"`
	}
	if err := paymentInternalRequest(c.Request.Context(), "/internal/hero-transfers", map[string]string{"action": "banks"}, &response); err != nil {
		driverError(c, 503, "banks_unavailable", "Bank verification is unavailable. You can still save details for manual payments.")
		return
	}
	heroData(c, response.Data)
}

func (a *adminAPI) auditHeroPayoutAction(c *gin.Context, action, id string) error {
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = adminAudit(c, tx, action, "", gin.H{"payoutId": id}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (a *adminAPI) resendHeroPayoutOTP(c *gin.Context) {
	id := c.Param("id")
	if _, err := uuid.Parse(id); err != nil {
		driverError(c, 400, "invalid_payout", "Select a valid payout")
		return
	}
	var result struct {
		Data struct {
			Submitted bool `json:"submitted"`
		} `json:"data"`
	}
	if err := paymentInternalRequest(c.Request.Context(), "/internal/hero-transfers", map[string]string{"id": id, "action": "resend"}, &result); err != nil {
		driverError(c, 409, "otp_resend_unconfirmed", "Monnify could not confirm resending the code. Check the payout status and try again.")
		return
	}
	if err := a.auditHeroPayoutAction(c, "heroes.payout.authorization.resent", id); err != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"submitted": true})
}

func (a *adminAPI) cancelQueuedHeroPayout(c *gin.Context) {
	id := c.Param("id")
	if _, err := uuid.Parse(id); err != nil {
		driverError(c, 400, "invalid_payout", "Select a valid payout")
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(ctx)
	var hero string
	if err = tx.QueryRow(ctx, `SELECT hero_id::text FROM hero_stage_payouts WHERE id=$1::uuid`, id).Scan(&hero); err != nil {
		driverError(c, 404, "payout_not_found", "Payout not found")
		return
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "hero-money:"+hero); err != nil {
		heroFailure(c)
		return
	}
	tag, err := tx.Exec(ctx, `UPDATE hero_stage_payouts SET status='CANCELLED',updated_at=now() WHERE id=$1::uuid AND status='queued'`, id)
	if err != nil {
		heroFailure(c)
		return
	}
	if tag.RowsAffected() != 1 {
		driverError(c, 409, "payout_already_submitted", "Only a queued transfer can be cancelled. Refresh and check its status before paying manually.")
		return
	}
	if err = adminAudit(c, tx, "heroes.payout.cancelled", "", gin.H{"payoutId": id}); err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"cancelled": true})
}
