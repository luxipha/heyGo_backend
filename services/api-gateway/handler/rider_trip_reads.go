package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"time"
)

type riderTripAPI struct{ pool *pgxpool.Pool }

func registerRiderTripReadRoutes(g *gin.RouterGroup, pool *pgxpool.Pool) {
	a := &riderTripAPI{pool: pool}
	r := g.Group("/rider", gatewayauth.RequireRole("rider"))
	r.GET("/trips", a.history)
	r.GET("/profile/metrics", a.profileMetrics)
	r.GET("/profile/feedback", a.profileFeedback)
	r.GET("/profile/trust-history", a.profileTrustHistory)
	r.GET("/trips/:tripID/settlement", a.settlement)
	r.GET("/trips/:tripID/receipt", a.receipt)
	r.GET("/trips/:tripID/rating", a.rating)
	r.GET("/trips/:tripID/no-show", a.noShow)
	r.GET("/trips/:tripID/sos/:requestID", a.sosReceipt)
	r.GET("/trips/:tripID/support-cases", a.supportCases)
}
func (a *riderTripAPI) owned(c *gin.Context) *riderTripSnapshot {
	id := c.Param("tripID")
	if _, err := uuid.Parse(id); err != nil {
		driverError(c, 400, "invalid_trip_id", "Valid trip ID is required")
		return nil
	}
	if a.pool == nil {
		driverError(c, 503, "trip_unavailable", "Trip is unavailable")
		return nil
	}
	t, err := (&riderHomeAPI{pool: a.pool}).readTrip(c, id, false)
	if err != nil {
		driverError(c, 503, "trip_unavailable", "Trip is unavailable")
		return nil
	}
	if t == nil {
		driverError(c, 404, "trip_not_found", "Trip was not found")
		return nil
	}
	return t
}
func (a *riderTripAPI) history(c *gin.Context) {
	if a.pool == nil {
		driverError(c, 503, "history_unavailable", "Ride history is unavailable")
		return
	}
	limit, err := parseDriverTripHistoryLimit(c.Query("limit"))
	if err != nil {
		driverError(c, 400, "invalid_limit", err.Error())
		return
	}
	var at, after any
	if raw := c.Query("cursor"); raw != "" {
		cursor, err := decodeDriverTripHistoryCursor(raw)
		if err != nil {
			driverError(c, 400, "invalid_cursor", "History cursor is invalid")
			return
		}
		at, after = cursor.OccurredAt, cursor.ID
	}
	user, _ := gatewayauth.CurrentUser(c)
	rows, err := a.pool.Query(c.Request.Context(), `SELECT id::TEXT,COALESCE(completed_at,cancelled_at,updated_at,created_at) FROM trips WHERE rider_id=$1::UUID AND status IN ('completed','cancelled') AND ($2::TIMESTAMPTZ IS NULL OR (COALESCE(completed_at,cancelled_at,updated_at,created_at),id)<($2,$3::UUID)) ORDER BY COALESCE(completed_at,cancelled_at,updated_at,created_at) DESC,id DESC LIMIT $4`, user.ID, at, after, limit+1)
	if err != nil {
		driverError(c, 503, "history_unavailable", "Ride history is unavailable")
		return
	}
	ids := []driverTripHistoryCursor{}
	for rows.Next() {
		var item driverTripHistoryCursor
		if err := rows.Scan(&item.ID, &item.OccurredAt); err != nil {
			rows.Close()
			driverError(c, 503, "history_unavailable", "Ride history is unavailable")
			return
		}
		ids = append(ids, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		driverError(c, 503, "history_unavailable", "Ride history is unavailable")
		return
	}
	var next *string
	if len(ids) > limit {
		v := encodeDriverTripHistoryCursor(ids[limit-1])
		next = &v
		ids = ids[:limit]
	}
	items := []gin.H{}
	home := &riderHomeAPI{pool: a.pool}
	for _, item := range ids {
		trip, err := home.readTrip(c, item.ID, false)
		if err != nil || trip == nil {
			driverError(c, 503, "history_unavailable", "Ride history is unavailable")
			return
		}
		items = append(items, gin.H{"trip": trip, "occurredAt": item.OccurredAt})
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"items": items, "nextCursor": next}})
}
func (a *riderTripAPI) settlementData(c *gin.Context, t *riderTripSnapshot) (gin.H, error) {
	user, _ := gatewayauth.CurrentUser(c)
	var state string
	var expected, fare, debt int64
	var updated time.Time
	var resolved *time.Time
	err := a.pool.QueryRow(c.Request.Context(), `SELECT status,expected_amount_kobo,fare_amount_kobo,no_show_debt_kobo,updated_at,resolved_at FROM trip_settlements WHERE trip_id=$1::UUID AND rider_id=$2::UUID`, t.ID, user.ID).Scan(&state, &expected, &fare, &debt, &updated, &resolved)
	if errors.Is(err, pgx.ErrNoRows) {
		return gin.H{"tripId": t.ID, "status": "unavailable", "currency": "NGN", "providerVerified": false}, nil
	}
	if err != nil {
		return nil, err
	}
	return gin.H{"tripId": t.ID, "status": state, "expectedAmountKobo": expected, "fareKobo": fare, "noShowDebtKobo": debt, "currency": "NGN", "confirmationSource": "driver_entered", "providerVerified": false, "updatedAt": updated, "resolvedAt": resolved}, nil
}
func (a *riderTripAPI) settlement(c *gin.Context) {
	t := a.owned(c)
	if t == nil {
		return
	}
	data, err := a.settlementData(c, t)
	if err != nil {
		driverError(c, 503, "settlement_unavailable", "Settlement is unavailable")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: data})
}
func (a *riderTripAPI) receipt(c *gin.Context) {
	t := a.owned(c)
	if t == nil {
		return
	}
	if t.Status != "completed" {
		driverError(c, 409, "receipt_unavailable", "Receipts are available for completed trips")
		return
	}
	settlement, err := a.settlementData(c, t)
	if err != nil {
		driverError(c, 503, "receipt_unavailable", "Receipt is unavailable")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"reference": "trip:" + t.ID, "trip": t, "fareKobo": t.FareKobo, "noShowDebtKobo": t.NoShowDebtKobo, "amountDueKobo": t.AmountDueKobo, "currency": "NGN", "settlement": settlement, "providerVerified": false}})
}
func (a *riderTripAPI) rating(c *gin.Context) {
	t := a.owned(c)
	if t == nil {
		return
	}
	user, _ := gatewayauth.CurrentUser(c)
	var rating int
	var comment, review string
	var created time.Time
	err := a.pool.QueryRow(c.Request.Context(), `SELECT rating,comment,admin_review_status,created_at FROM trip_ratings WHERE trip_id=$1::UUID AND actor_id=$2::UUID`, t.ID, user.ID).Scan(&rating, &comment, &review, &created)
	var submitted any
	if err == nil {
		submitted = gin.H{"rating": rating, "comment": comment, "reviewStatus": review, "submittedAt": created}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 503, "rating_unavailable", "Rating is unavailable")
		return
	}
	var driver map[string]any
	_ = json.Unmarshal(t.Driver, &driver)
	eligible := t.Status == "completed" && driver != nil && driver["id"] != nil && driver["id"] != user.ID && submitted == nil
	c.JSON(200, contracts.APIResponse{Data: gin.H{"tripId": t.ID, "eligible": eligible, "submitted": submitted}})
}
func (a *riderTripAPI) noShow(c *gin.Context) {
	t := a.owned(c)
	if t == nil {
		return
	}
	user, _ := gatewayauth.CurrentUser(c)
	var id, state string
	var claimed int64
	var debt *int64
	var debtState *string
	var submitted time.Time
	err := a.pool.QueryRow(c.Request.Context(), `SELECT c.id::TEXT,c.status,c.fee_kobo,c.submitted_at,d.amount_kobo,d.status FROM driver_no_show_claims c LEFT JOIN rider_no_show_debts d ON d.claim_id=c.id WHERE c.trip_id=$1::UUID AND c.rider_id=$2::UUID`, t.ID, user.ID).Scan(&id, &state, &claimed, &submitted, &debt, &debtState)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(200, contracts.APIResponse{Data: gin.H{"tripId": t.ID, "claim": nil, "approvedDebtKobo": 0, "currency": "NGN"}})
		return
	}
	if err != nil {
		driverError(c, 503, "no_show_unavailable", "No-show status is unavailable")
		return
	}
	approved := int64(0)
	if debt != nil && debtState != nil && (*debtState == "due" || *debtState == "settled") {
		approved = *debt
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"tripId": t.ID, "claim": gin.H{"id": id, "status": state, "claimedFeeKobo": claimed, "submittedAt": submitted}, "approvedDebtKobo": approved, "debtStatus": debtState, "currency": "NGN"}})
}

func (a *riderTripAPI) sosReceipt(c *gin.Context) {
	t := a.owned(c)
	if t == nil {
		return
	}
	id := c.Param("requestID")
	if _, err := uuid.Parse(id); err != nil {
		driverError(c, 400, "invalid_request_id", "Valid SOS request identity is required")
		return
	}
	user, _ := gatewayauth.CurrentUser(c)
	var incident, caseID, incidentStatus string
	err := a.pool.QueryRow(c.Request.Context(), `SELECT i.id::TEXT,i.case_id::TEXT,i.status FROM sos_intakes s JOIN safety_incidents i ON i.id=s.incident_id WHERE s.trip_id=$1::UUID AND s.reporter_id=$2::UUID AND s.client_request_id=$3::UUID`, t.ID, user.ID, id).Scan(&incident, &caseID, &incidentStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "sos_not_found", "SOS intake was not found")
		return
	}
	if err != nil {
		driverError(c, 503, "sos_unavailable", "SOS intake is unavailable")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"tripId": t.ID, "requestId": id, "incidentId": incident, "caseId": caseID, "status": "received", "incidentStatus": incidentStatus}})
}

func (a *riderTripAPI) supportCases(c *gin.Context) {
	t := a.owned(c)
	if t == nil {
		return
	}
	user, _ := gatewayauth.CurrentUser(c)
	rows, err := a.pool.Query(c.Request.Context(), `SELECT id::TEXT,case_number,status,subject FROM support_cases WHERE trip_id=$1::UUID AND opened_by=$2::UUID AND audience='rider' ORDER BY created_at DESC,id DESC LIMIT 25`, t.ID, user.ID)
	if err != nil {
		driverError(c, 503, "support_unavailable", "Linked Support cases are unavailable")
		return
	}
	defer rows.Close()
	items := []gin.H{}
	for rows.Next() {
		var id, status, subject string
		var number int64
		if err := rows.Scan(&id, &number, &status, &subject); err != nil {
			driverError(c, 503, "support_unavailable", "Linked Support cases are unavailable")
			return
		}
		items = append(items, gin.H{"id": id, "reference": fmt.Sprintf("HG-%08d", number), "status": status, "subject": subject, "tripId": t.ID})
	}
	if rows.Err() != nil {
		driverError(c, 503, "support_unavailable", "Linked Support cases are unavailable")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"items": items}})
}
