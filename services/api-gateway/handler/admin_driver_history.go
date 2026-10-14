package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

type staffDocumentHistory struct {
	ID              string     `json:"id"`
	Type            string     `json:"type"`
	Status          string     `json:"status"`
	RejectionReason string     `json:"rejectionReason"`
	SubmittedAt     time.Time  `json:"submittedAt"`
	ReviewedAt      *time.Time `json:"reviewedAt,omitempty"`
	ReviewedBy      *string    `json:"reviewedBy,omitempty"`
	ExpiresAt       *time.Time `json:"expiresAt,omitempty"`
}

type staffInspectionHistory struct {
	ID               string     `json:"id"`
	Status           string     `json:"status"`
	PartnerName      string     `json:"partnerName"`
	LocationName     string     `json:"locationName"`
	PartnerReference string     `json:"partnerReference"`
	OutcomeReason    string     `json:"outcomeReason"`
	ReportReceivedAt *time.Time `json:"reportReceivedAt,omitempty"`
	InspectedAt      *time.Time `json:"inspectedAt,omitempty"`
	ReviewedBy       *string    `json:"reviewedBy,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
}

func registerAdminDriverHistoryRoutes(group *gin.RouterGroup, a *adminAPI) {
	group.GET("/drivers/:driverID/documents/history", a.driverDocumentHistory)
	group.GET("/drivers/:driverID/inspections/history", a.driverInspectionHistory)
}

func (a *adminAPI) driverDocumentHistory(ctx *gin.Context) {
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	limit, at, id, ok := staffPage(ctx)
	if !ok {
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT id::TEXT,type,status,rejection_reason,submitted_at,reviewed_at,reviewed_by::TEXT,expires_at FROM driver_documents WHERE driver_id=$1::UUID AND ($2::TIMESTAMPTZ IS NULL OR (submitted_at,id)<($2,$3::UUID)) ORDER BY submitted_at DESC,id DESC LIMIT $4`, driver, at, id, limit+1)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "review_unavailable", "Document history is unavailable")
		return
	}
	defer rows.Close()
	items := make([]staffDocumentHistory, 0, limit+1)
	for rows.Next() {
		var item staffDocumentHistory
		if rows.Scan(&item.ID, &item.Type, &item.Status, &item.RejectionReason, &item.SubmittedAt, &item.ReviewedAt, &item.ReviewedBy, &item.ExpiresAt) != nil {
			driverError(ctx, http.StatusServiceUnavailable, "review_unavailable", "Document history is unavailable")
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "review_unavailable", "Document history is unavailable")
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		encoded := encodeDriverTripHistoryCursor(driverTripHistoryCursor{OccurredAt: items[len(items)-1].SubmittedAt, ID: items[len(items)-1].ID})
		next = &encoded
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: items, Meta: contracts.APIMeta{NextCursor: next}})
}

func (a *adminAPI) driverInspectionHistory(ctx *gin.Context) {
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	limit, at, id, ok := staffPage(ctx)
	if !ok {
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT id::TEXT,status,partner_name,location_name,partner_reference,outcome_reason,report_received_at,inspected_at,reviewed_by::TEXT,created_at FROM driver_inspections WHERE driver_id=$1::UUID AND ($2::TIMESTAMPTZ IS NULL OR (created_at,id)<($2,$3::UUID)) ORDER BY created_at DESC,id DESC LIMIT $4`, driver, at, id, limit+1)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "review_unavailable", "Inspection history is unavailable")
		return
	}
	defer rows.Close()
	items := make([]staffInspectionHistory, 0, limit+1)
	for rows.Next() {
		var item staffInspectionHistory
		if rows.Scan(&item.ID, &item.Status, &item.PartnerName, &item.LocationName, &item.PartnerReference, &item.OutcomeReason, &item.ReportReceivedAt, &item.InspectedAt, &item.ReviewedBy, &item.CreatedAt) != nil {
			driverError(ctx, http.StatusServiceUnavailable, "review_unavailable", "Inspection history is unavailable")
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "review_unavailable", "Inspection history is unavailable")
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
