package handler

import (
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

type pricingRateInput struct {
	PackageSlug   string `json:"packageSlug"`
	BaseKobo      int64  `json:"baseKobo"`
	PerKmKobo     int64  `json:"perKmKobo"`
	PerMinuteKobo int64  `json:"perMinuteKobo"`
}

func registerAdminPricingRoutes(g *gin.RouterGroup, a *adminAPI) {
	g.GET("/pricing", a.listPricing)
	g.GET("/pricing/:id", a.pricingDetail)
	g.POST("/pricing", a.requireCSRF, a.createPricing)
	g.POST("/pricing/:id/simulate", a.requireCSRF, a.simulatePricing)
	g.POST("/pricing/:id/approve", a.requireCSRF, a.approvePricing)
	g.POST("/pricing/:id/retirement-requests", a.requireCSRF, a.requestPricingRetirement)
	g.POST("/pricing/:id/retirement-requests/:requestID/approve", a.requireCSRF, a.approvePricingRetirement)
}

func (a *adminAPI) listPricing(c *gin.Context) {
	market := strings.ToUpper(strings.TrimSpace(c.Query("marketCode")))
	status := c.DefaultQuery("status", "all")
	if market != "" && !geofenceCodePattern.MatchString(market) || status != "all" && status != "draft" && status != "approved" {
		driverError(c, 400, "invalid_pricing_filter", "Valid market and status are required")
		return
	}
	rows, err := a.pool.Query(c.Request.Context(), `SELECT id::TEXT,market_code,version,status,effective_from,effective_until,created_by::TEXT,approved_by::TEXT,approved_at,created_at FROM pricing_versions WHERE ($1='' OR market_code=$1) AND ($2='all' OR status=$2) ORDER BY created_at DESC,id DESC LIMIT 101`, market, status)
	if err != nil {
		reviewFailure(c)
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var id, code, state, creator string
		var version int
		var from, created time.Time
		var until, approvedAt *time.Time
		var approver *string
		if rows.Scan(&id, &code, &version, &state, &from, &until, &creator, &approver, &approvedAt, &created) != nil {
			reviewFailure(c)
			return
		}
		items = append(items, gin.H{"id": id, "marketCode": code, "version": version, "status": state, "effectiveFrom": from, "effectiveUntil": until, "proposedBy": creator, "approvedBy": approver, "approvedAt": approvedAt, "createdAt": created, "mayApprove": adminHasPermission(c, permissionFinanceWrite) && state == "draft" && creator != currentAdmin(c).ID})
	}
	if rows.Err() != nil {
		reviewFailure(c)
		return
	}
	if len(items) > 100 {
		driverError(c, 409, "pricing_page_limit", "Use marketCode to narrow the pricing list")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: items})
}

func (a *adminAPI) pricingDetail(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		driverError(c, 400, "invalid_pricing_id", "Valid pricing ID is required")
		return
	}
	var code, state, creator string
	var version int
	var from, created time.Time
	var until, approvedAt *time.Time
	var approver *string
	err = a.pool.QueryRow(c.Request.Context(), `SELECT market_code,version,status,effective_from,effective_until,created_by::TEXT,approved_by::TEXT,approved_at,created_at FROM pricing_versions WHERE id=$1`, id).Scan(&code, &version, &state, &from, &until, &creator, &approver, &approvedAt, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "pricing_not_found", "Pricing was not found")
		return
	}
	if err != nil {
		reviewFailure(c)
		return
	}
	rows, err := a.pool.Query(c.Request.Context(), `SELECT package_slug,base_kobo,per_km_kobo,per_minute_kobo FROM pricing_package_rates WHERE pricing_id=$1 ORDER BY package_slug`, id)
	if err != nil {
		reviewFailure(c)
		return
	}
	defer rows.Close()
	rates := make([]pricingRateInput, 0, 4)
	for rows.Next() {
		var rate pricingRateInput
		if rows.Scan(&rate.PackageSlug, &rate.BaseKobo, &rate.PerKmKobo, &rate.PerMinuteKobo) != nil {
			reviewFailure(c)
			return
		}
		rates = append(rates, rate)
	}
	if rows.Err() != nil {
		reviewFailure(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"id": id.String(), "marketCode": code, "version": version, "status": state, "effectiveFrom": from, "effectiveUntil": until, "proposedBy": creator, "approvedBy": approver, "approvedAt": approvedAt, "createdAt": created, "rates": rates, "mayApprove": adminHasPermission(c, permissionFinanceWrite) && state == "draft" && creator != currentAdmin(c).ID}})
}

func (a *adminAPI) createPricing(c *gin.Context) {
	var b struct {
		MarketCode     string             `json:"marketCode"`
		EffectiveFrom  time.Time          `json:"effectiveFrom"`
		EffectiveUntil *time.Time         `json:"effectiveUntil"`
		Rates          []pricingRateInput `json:"rates"`
	}
	if c.ShouldBindJSON(&b) != nil {
		driverError(c, 400, "invalid_pricing", "Valid pricing input is required")
		return
	}
	b.MarketCode = strings.ToUpper(strings.TrimSpace(b.MarketCode))
	if !geofenceCodePattern.MatchString(b.MarketCode) || b.EffectiveFrom.IsZero() || b.EffectiveUntil != nil && !b.EffectiveUntil.After(b.EffectiveFrom) || (len(b.Rates) < 1 || len(b.Rates) > 100) {
		driverError(c, 400, "invalid_pricing", "Market, period, and category rates are required")
		return
	}
	seen := map[string]bool{}
	for _, rate := range b.Rates {
		if strings.TrimSpace(rate.PackageSlug) == "" || seen[rate.PackageSlug] || rate.BaseKobo < 0 || rate.BaseKobo > 1000000 || rate.PerKmKobo < 0 || rate.PerKmKobo > 10000 || rate.PerMinuteKobo < 0 || rate.PerMinuteKobo > 10000 {
			driverError(c, 400, "invalid_pricing", "Unique package rates must be within kobo bounds")
			return
		}
		seen[rate.PackageSlug] = true
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		reviewFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	if _, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(hashtext('pricing:'||$1))`, b.MarketCode); err != nil {
		reviewFailure(c)
		return
	}
	var marketExists bool
	if err = tx.QueryRow(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM trip_geofences WHERE kind='market' AND code=$1 AND status='approved')`, b.MarketCode).Scan(&marketExists); err != nil {
		reviewFailure(c)
		return
	}
	if !marketExists {
		driverError(c, 409, "market_unpublished", "Published market is required before pricing draft")
		return
	}
	var version int
	if err = tx.QueryRow(c.Request.Context(), `SELECT COALESCE(MAX(version),0)+1 FROM pricing_versions WHERE market_code=$1`, b.MarketCode).Scan(&version); err != nil {
		reviewFailure(c)
		return
	}
	id := uuid.New()
	_, err = tx.Exec(c.Request.Context(), `INSERT INTO pricing_versions(id,market_code,version,effective_from,effective_until,created_by) VALUES($1,$2,$3,$4,$5,$6::UUID)`, id, b.MarketCode, version, b.EffectiveFrom.UTC(), b.EffectiveUntil, currentAdmin(c).ID)
	if err != nil {
		reviewFailure(c)
		return
	}
	for _, rate := range b.Rates {
		var valid bool
		if err = tx.QueryRow(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM ride_categories WHERE slug=$1 AND status<>'archived')`, rate.PackageSlug).Scan(&valid); err != nil {
			reviewFailure(c)
			return
		}
		if !valid {
			driverError(c, 400, "invalid_pricing_category", "Choose an existing, non-archived ride category")
			return
		}
		if _, err = tx.Exec(c.Request.Context(), `INSERT INTO pricing_package_rates(pricing_id,package_slug,base_kobo,per_km_kobo,per_minute_kobo) VALUES($1,$2,$3,$4,$5)`, id, rate.PackageSlug, rate.BaseKobo, rate.PerKmKobo, rate.PerMinuteKobo); err != nil {
			reviewFailure(c)
			return
		}
	}
	if adminAudit(c, tx, "pricing.draft.created", "", gin.H{"policyId": id.String(), "marketCode": b.MarketCode, "version": version}) != nil || tx.Commit(c.Request.Context()) != nil {
		reviewFailure(c)
		return
	}
	c.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": id.String(), "version": version, "status": "draft"}})
}

func (a *adminAPI) simulatePricing(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		driverError(c, 400, "invalid_pricing_id", "Valid pricing ID is required")
		return
	}
	var b struct {
		DistanceMeters  float64 `json:"distanceMeters"`
		DurationSeconds float64 `json:"durationSeconds"`
	}
	if c.ShouldBindJSON(&b) != nil || math.IsNaN(b.DistanceMeters) || math.IsInf(b.DistanceMeters, 0) || math.IsNaN(b.DurationSeconds) || math.IsInf(b.DurationSeconds, 0) || b.DistanceMeters < 0 || b.DistanceMeters > 1000000 || b.DurationSeconds < 0 || b.DurationSeconds > 86400 {
		driverError(c, 400, "invalid_simulation", "Distance and duration are out of bounds")
		return
	}
	rows, err := a.pool.Query(c.Request.Context(), `SELECT r.package_slug,r.base_kobo,r.per_km_kobo,r.per_minute_kobo FROM pricing_package_rates r JOIN pricing_versions p ON p.id=r.pricing_id WHERE p.id=$1 ORDER BY r.package_slug`, id)
	if err != nil {
		reviewFailure(c)
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, 4)
	for rows.Next() {
		var rate pricingRateInput
		if rows.Scan(&rate.PackageSlug, &rate.BaseKobo, &rate.PerKmKobo, &rate.PerMinuteKobo) != nil {
			reviewFailure(c)
			return
		}
		amount := math.Round(float64(rate.BaseKobo) + b.DistanceMeters*float64(rate.PerKmKobo)/1000 + b.DurationSeconds*float64(rate.PerMinuteKobo)/60)
		items = append(items, gin.H{"packageSlug": rate.PackageSlug, "fareKobo": amount, "withinBounds": amount > 0 && amount <= 10000000})
	}
	if rows.Err() != nil {
		reviewFailure(c)
		return
	}
	if len(items) == 0 {
		driverError(c, 404, "pricing_not_found", "Pricing was not found")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"pricingId": id.String(), "distanceMeters": b.DistanceMeters, "durationSeconds": b.DurationSeconds, "fares": items}})
}

func (a *adminAPI) approvePricing(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		driverError(c, 400, "invalid_pricing_id", "Valid pricing ID is required")
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		reviewFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	var market, state, creator string
	var from time.Time
	var until *time.Time
	err = tx.QueryRow(c.Request.Context(), `SELECT market_code,status,created_by::TEXT,effective_from,effective_until FROM pricing_versions WHERE id=$1 FOR UPDATE`, id).Scan(&market, &state, &creator, &from, &until)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "pricing_not_found", "Pricing was not found")
		return
	}
	if err != nil {
		reviewFailure(c)
		return
	}
	if state != "draft" || creator == currentAdmin(c).ID || until != nil && !until.After(time.Now()) {
		driverError(c, 409, "pricing_approval_unavailable", "A different staff member must approve a current draft")
		return
	}
	if _, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(hashtext('pricing:'||$1))`, market); err != nil {
		reviewFailure(c)
		return
	}
	var count int
	err = tx.QueryRow(c.Request.Context(), `SELECT COUNT(*) FROM pricing_versions WHERE id<>$1 AND market_code=$2 AND status='approved' AND (effective_until IS NULL OR effective_until>$3) AND ($4::TIMESTAMPTZ IS NULL OR $4>effective_from)`, id, market, from, until).Scan(&count)
	if err != nil {
		reviewFailure(c)
		return
	}
	if count != 0 {
		driverError(c, 409, "pricing_overlap", "Published market pricing overlaps this period")
		return
	}
	var rateCount int
	if err = tx.QueryRow(c.Request.Context(), `SELECT COUNT(*) FROM pricing_package_rates WHERE pricing_id=$1`, id).Scan(&rateCount); err != nil {
		reviewFailure(c)
		return
	}
	if rateCount < 1 {
		driverError(c, 409, "pricing_incomplete", "At least one category rate is required")
		return
	}
	_, err = tx.Exec(c.Request.Context(), `UPDATE pricing_versions SET status='approved',approved_by=$2::UUID,approved_at=NOW() WHERE id=$1`, id, currentAdmin(c).ID)
	if err != nil || adminAudit(c, tx, "pricing.approved", "", gin.H{"policyId": id.String(), "marketCode": market}) != nil || tx.Commit(c.Request.Context()) != nil {
		reviewFailure(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"id": id.String(), "status": "approved", "proposedBy": creator, "approvedBy": currentAdmin(c).ID}})
}

func (a *adminAPI) requestPricingRetirement(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		driverError(c, 400, "invalid_pricing_id", "Valid pricing ID is required")
		return
	}
	var b struct {
		EffectiveUntil time.Time `json:"effectiveUntil"`
	}
	if c.ShouldBindJSON(&b) != nil || !b.EffectiveUntil.After(time.Now()) {
		driverError(c, 400, "invalid_retirement", "Future retirement time is required")
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		reviewFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	requestID := uuid.New()
	tag, err := tx.Exec(c.Request.Context(), `INSERT INTO pricing_retirements(id,pricing_id,effective_until,requested_by,status) SELECT $1,id,$3,$4::UUID,'pending' FROM pricing_versions WHERE id=$2 AND status='approved' AND effective_until IS NULL AND effective_from<$3`, requestID, id, b.EffectiveUntil.UTC(), currentAdmin(c).ID)
	if err != nil || tag.RowsAffected() != 1 {
		driverError(c, 409, "retirement_unavailable", "Published pricing cannot be retired")
		return
	}
	if adminAudit(c, tx, "pricing.retirement.requested", "", gin.H{"policyId": id.String(), "requestId": requestID.String()}) != nil || tx.Commit(c.Request.Context()) != nil {
		reviewFailure(c)
		return
	}
	c.JSON(201, contracts.APIResponse{Data: gin.H{"id": requestID.String(), "status": "pending"}})
}

func (a *adminAPI) approvePricingRetirement(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		driverError(c, 400, "invalid_pricing_id", "Valid pricing ID is required")
		return
	}
	requestID, err := uuid.Parse(c.Param("requestID"))
	if err != nil {
		driverError(c, 400, "invalid_retirement_id", "Valid retirement ID is required")
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		reviewFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	var requester, market string
	var until time.Time
	err = tx.QueryRow(c.Request.Context(), `SELECT r.requested_by::TEXT,r.effective_until,p.market_code FROM pricing_retirements r JOIN pricing_versions p ON p.id=r.pricing_id WHERE r.id=$1 AND r.pricing_id=$2 AND r.status='pending' FOR UPDATE OF r`, requestID, id).Scan(&requester, &until, &market)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "retirement_not_found", "Retirement request was not found")
		return
	}
	if err != nil {
		reviewFailure(c)
		return
	}
	if requester == currentAdmin(c).ID || !until.After(time.Now()) {
		driverError(c, 409, "retirement_approval_unavailable", "A different staff member must approve before retirement time")
		return
	}
	if _, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(hashtext('pricing:'||$1))`, market); err != nil {
		reviewFailure(c)
		return
	}
	tag, err := tx.Exec(c.Request.Context(), `UPDATE pricing_versions SET effective_until=$2 WHERE id=$1 AND status='approved' AND effective_until IS NULL`, id, until)
	if err != nil || tag.RowsAffected() != 1 {
		driverError(c, 409, "retirement_unavailable", "Published pricing cannot be retired")
		return
	}
	_, err = tx.Exec(c.Request.Context(), `UPDATE pricing_retirements SET status='approved',approved_by=$2::UUID,approved_at=NOW() WHERE id=$1`, requestID, currentAdmin(c).ID)
	if err != nil || adminAudit(c, tx, "pricing.retirement.approved", "", gin.H{"policyId": id.String(), "requestId": requestID.String()}) != nil || tx.Commit(c.Request.Context()) != nil {
		reviewFailure(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"id": requestID.String(), "status": "approved", "pricingId": id.String(), "effectiveUntil": until, "proposedBy": requester, "approvedBy": currentAdmin(c).ID}})
}
