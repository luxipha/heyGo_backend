package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/cprakhar/uber-clone/shared/statutory"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func registerAdminStatutoryChargeRoutes(g *gin.RouterGroup, a *adminAPI) {
	g.GET("/statutory-charge-rules", a.listStatutoryRules)
	g.GET("/statutory-charge-rules/:id", a.statutoryRuleDetail)
	g.POST("/statutory-charge-rules", a.requireCSRF, a.createStatutoryRule)
	g.POST("/statutory-charge-rules/:id/approve", a.requireCSRF, a.approveStatutoryRule)
	g.POST("/statutory-charge-rules/:id/retirement-requests", a.requireCSRF, a.requestStatutoryRuleRetirement)
	g.POST("/statutory-charge-rules/:id/retirement-requests/:requestID/approve", a.requireCSRF, a.approveStatutoryRuleRetirement)
}

type statutoryRuleInput struct {
	LedgerCode        string          `json:"ledgerCode"`
	Name              string          `json:"name"`
	Authority         string          `json:"authority"`
	Jurisdiction      string          `json:"jurisdiction"`
	CalculationType   string          `json:"calculationType"`
	CalculationBase   string          `json:"calculationBase"`
	CalculationConfig json.RawMessage `json:"calculationConfig"`
	CountryCode       *string         `json:"countryCode"`
	RegionCode        *string         `json:"regionCode"`
	RegionMatch       string          `json:"regionMatch"`
	MarketCode        *string         `json:"marketCode"`
	RegulatoryTag     *string         `json:"regulatoryTag"`
	VehiclePackage    *string         `json:"vehiclePackage"`
	Bearer            string          `json:"bearer"`
	FundingSource     string          `json:"fundingSource"`
	MinimumKobo       *int64          `json:"minimumKobo"`
	MaximumKobo       *int64          `json:"maximumKobo"`
	EffectiveFrom     time.Time       `json:"effectiveFrom"`
	EffectiveUntil    *time.Time      `json:"effectiveUntil"`
	Reference         string          `json:"reference"`
	Notes             string          `json:"notes"`
}

func (b *statutoryRuleInput) normalize() error {
	b.LedgerCode = strings.ToUpper(strings.TrimSpace(b.LedgerCode))
	b.Name = strings.TrimSpace(b.Name)
	b.Authority = strings.TrimSpace(b.Authority)
	b.Jurisdiction = strings.TrimSpace(b.Jurisdiction)
	if b.LedgerCode == "" || len(b.LedgerCode) > 40 || !geofenceCodePattern.MatchString(b.LedgerCode) || b.Name == "" || len(b.Name) > 120 || b.Authority == "" || len(b.Authority) > 120 || b.Jurisdiction == "" || len(b.Jurisdiction) > 80 {
		return errors.New("identity")
	}
	if b.CalculationBase != "trip_fare" || b.Bearer != "driver" && b.Bearer != "rider" && b.Bearer != "platform" || b.FundingSource != "operating_balance" && b.FundingSource != "rider_payment" && b.FundingSource != "platform" {
		return errors.New("calculation or funding")
	}
	if (b.Bearer == "driver" && b.FundingSource != "operating_balance") || (b.Bearer == "rider" && b.FundingSource != "rider_payment") || (b.Bearer == "platform" && b.FundingSource != "platform") {
		return errors.New("funding must match charge bearer")
	}
	if b.RegionMatch == "" {
		b.RegionMatch = "either"
	}
	if b.RegionMatch != "pickup" && b.RegionMatch != "destination" && b.RegionMatch != "either" && b.RegionMatch != "both" {
		return errors.New("region match")
	}
	if b.CalculationType != "fixed" && b.CalculationType != "percentage" && b.CalculationType != "tiered" && b.CalculationType != "formula" {
		return errors.New("calculation type")
	}
	if len(b.CalculationConfig) == 0 || !json.Valid(b.CalculationConfig) || len(b.CalculationConfig) > 8192 {
		return errors.New("calculation config")
	}
	if b.MinimumKobo != nil && (*b.MinimumKobo < 0 || *b.MinimumKobo%100 != 0) || b.MaximumKobo != nil && (*b.MaximumKobo < 0 || *b.MaximumKobo%100 != 0) || b.MinimumKobo != nil && b.MaximumKobo != nil && *b.MaximumKobo < *b.MinimumKobo {
		return errors.New("bounds")
	}
	if b.EffectiveFrom.IsZero() || b.EffectiveUntil != nil && !b.EffectiveUntil.After(b.EffectiveFrom) {
		return errors.New("dates")
	}
	_, err := statutory.Calculate(statutory.Rule{Type: b.CalculationType, Base: b.CalculationBase, Config: b.CalculationConfig, Minimum: b.MinimumKobo, Maximum: b.MaximumKobo}, 100000)
	if err != nil {
		return err
	}
	for _, v := range []*string{b.CountryCode, b.RegionCode, b.MarketCode, b.RegulatoryTag, b.VehiclePackage} {
		if v != nil {
			*v = strings.ToUpper(strings.TrimSpace(*v))
			if *v == "" || len(*v) > 40 {
				return errors.New("selector")
			}
		}
	}
	if b.CountryCode != nil && *b.CountryCode != "NG" {
		return errors.New("country scope is not supported by current trip classification")
	}
	return nil
}

func (a *adminAPI) listStatutoryRules(c *gin.Context) {
	status := c.DefaultQuery("status", "all")
	if status != "all" && status != "draft" && status != "approved" {
		driverError(c, 400, "invalid_rule_filter", "Invalid rule status")
		return
	}
	code := strings.ToUpper(strings.TrimSpace(c.Query("ledgerCode")))
	if code != "" && !geofenceCodePattern.MatchString(code) {
		driverError(c, 400, "invalid_rule_filter", "Invalid ledger code")
		return
	}
	cursor := c.Query("cursor")
	if cursor != "" {
		if _, err := uuid.Parse(cursor); err != nil {
			driverError(c, 400, "invalid_cursor", "Valid cursor is required")
			return
		}
	}
	rows, err := a.pool.Query(c, `SELECT r.id::TEXT,r.ledger_code,r.version,r.name,r.calculation_type,r.calculation_base,r.region_match,r.market_code,r.region_code,r.bearer,r.funding_source,r.status,r.effective_from,r.effective_until,r.created_at FROM statutory_charge_rules r WHERE ($1='all' OR r.status=$1) AND ($2='' OR r.ledger_code=$2) AND ($3='' OR (r.created_at,r.id)<(SELECT x.created_at,x.id FROM statutory_charge_rules x WHERE x.id=NULLIF($3,'')::UUID)) ORDER BY r.created_at DESC,r.id DESC LIMIT 51`, status, code, cursor)
	if err != nil {
		driverError(c, 503, "statutory_rules_unavailable", "Statutory charge rules are unavailable")
		return
	}
	defer rows.Close()
	out := make([]gin.H, 0)
	for rows.Next() {
		var id, ledger, name, typ, base, match, bearer, funding, state string
		var version int
		var market, region *string
		var from, created time.Time
		var until *time.Time
		if rows.Scan(&id, &ledger, &version, &name, &typ, &base, &match, &market, &region, &bearer, &funding, &state, &from, &until, &created) != nil {
			driverError(c, 503, "statutory_rules_unavailable", "Statutory charge rules are unavailable")
			return
		}
		out = append(out, gin.H{"id": id, "ledgerCode": ledger, "version": version, "name": name, "calculationType": typ, "calculationBase": base, "regionMatch": match, "marketCode": market, "regionCode": region, "bearer": bearer, "fundingSource": funding, "status": state, "effectiveFrom": from, "effectiveUntil": until, "createdAt": created})
	}
	if rows.Err() != nil {
		driverError(c, 503, "statutory_rules_unavailable", "Statutory charge rules are unavailable")
		return
	}
	var next *string
	if len(out) > 50 {
		v := out[49]["id"].(string)
		next = &v
		out = out[:50]
	}
	c.JSON(200, contracts.APIResponse{Data: out, Meta: contracts.APIMeta{NextCursor: next}})
}
func (a *adminAPI) statutoryRuleDetail(c *gin.Context) {
	id, ok := geofenceID(c, "id")
	if !ok {
		return
	}
	var ledger, name, authority, jur, typ, base, match, bearer, funding, state, ref, notes, creator string
	var version int
	var config []byte
	var country, region, market, tag, pkg *string
	var min, max *int64
	var from, created time.Time
	var until, approvedAt *time.Time
	var approver *string
	err := a.pool.QueryRow(c, `SELECT ledger_code,version,name,authority,jurisdiction,calculation_type,calculation_base,calculation_config,country_code,region_code,region_match,market_code,regulatory_tag,vehicle_package,bearer,funding_source,minimum_kobo,maximum_kobo,status,effective_from,effective_until,reference,notes,created_by::TEXT,approved_by::TEXT,approved_at,created_at FROM statutory_charge_rules WHERE id=$1::UUID`, id).Scan(&ledger, &version, &name, &authority, &jur, &typ, &base, &config, &country, &region, &match, &market, &tag, &pkg, &bearer, &funding, &min, &max, &state, &from, &until, &ref, &notes, &creator, &approver, &approvedAt, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "statutory_rule_not_found", "Statutory charge rule was not found")
		return
	}
	if err != nil {
		driverError(c, 503, "statutory_rules_unavailable", "Statutory charge rule is unavailable")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"id": id, "ledgerCode": ledger, "version": version, "name": name, "authority": authority, "jurisdiction": jur, "calculationType": typ, "calculationBase": base, "calculationConfig": json.RawMessage(config), "countryCode": country, "regionCode": region, "regionMatch": match, "marketCode": market, "regulatoryTag": tag, "vehiclePackage": pkg, "bearer": bearer, "fundingSource": funding, "minimumKobo": min, "maximumKobo": max, "status": state, "effectiveFrom": from, "effectiveUntil": until, "reference": ref, "notes": notes, "createdBy": creator, "approvedBy": approver, "approvedAt": approvedAt, "createdAt": created}})
}
func (a *adminAPI) createStatutoryRule(c *gin.Context) {
	var b statutoryRuleInput
	if c.ShouldBindJSON(&b) != nil || b.normalize() != nil {
		driverError(c, 400, "invalid_statutory_rule", "Rule fields or calculation configuration are invalid")
		return
	}
	tx, err := a.pool.Begin(c)
	if err != nil {
		reviewFailure(c)
		return
	}
	defer tx.Rollback(c)
	if _, err = tx.Exec(c, `SELECT pg_advisory_xact_lock(hashtext($1))`, `statutory-rule:`+b.LedgerCode); err != nil {
		reviewFailure(c)
		return
	}
	var version int
	if err = tx.QueryRow(c, `SELECT COALESCE(MAX(version),0)+1 FROM statutory_charge_rules WHERE ledger_code=$1`, b.LedgerCode).Scan(&version); err != nil {
		reviewFailure(c)
		return
	}
	id := uuid.NewString()
	_, err = tx.Exec(c, `INSERT INTO statutory_charge_rules(id,ledger_code,version,name,authority,jurisdiction,calculation_type,calculation_base,calculation_config,country_code,region_code,region_match,market_code,regulatory_tag,vehicle_package,bearer,funding_source,minimum_kobo,maximum_kobo,status,effective_from,effective_until,reference,notes,created_by)
	VALUES($1::UUID,$2,$3,$4,$5,$6,$7,$8,$9::JSONB,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,'draft',$20,$21,$22,$23,$24::UUID)`, id, b.LedgerCode, version, b.Name, b.Authority, b.Jurisdiction, b.CalculationType, b.CalculationBase, b.CalculationConfig, b.CountryCode, b.RegionCode, b.RegionMatch, b.MarketCode, b.RegulatoryTag, b.VehiclePackage, b.Bearer, b.FundingSource, b.MinimumKobo, b.MaximumKobo, b.EffectiveFrom.UTC(), b.EffectiveUntil, b.Reference, b.Notes, currentAdmin(c).ID)
	if err != nil || adminAudit(c, tx, "statutory_charge_rule_draft_created", "", gin.H{"ruleId": id, "ledgerCode": b.LedgerCode, "version": version}) != nil || tx.Commit(c) != nil {
		reviewFailure(c)
		return
	}
	c.JSON(201, contracts.APIResponse{Data: gin.H{"id": id, "status": "draft", "version": version}})
}
func (a *adminAPI) approveStatutoryRule(c *gin.Context) {
	id, ok := geofenceID(c, "id")
	if !ok {
		return
	}
	tx, err := a.pool.Begin(c)
	if err != nil {
		reviewFailure(c)
		return
	}
	defer tx.Rollback(c)
	var ledger, state, creator, bearer, funding string
	var version int
	var from time.Time
	var until *time.Time
	err = tx.QueryRow(c, `SELECT ledger_code,version,status,created_by::TEXT,effective_from,effective_until,bearer,funding_source FROM statutory_charge_rules WHERE id=$1::UUID FOR UPDATE`, id).Scan(&ledger, &version, &state, &creator, &from, &until, &bearer, &funding)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "statutory_rule_not_found", "Statutory charge rule was not found")
		return
	}
	if err != nil {
		reviewFailure(c)
		return
	}
	if state != "draft" || creator == currentAdmin(c).ID || (until != nil && !until.After(time.Now())) {
		driverError(c, 409, "statutory_rule_approval_unavailable", "A different staff member must approve a current draft")
		return
	}
	if bearer != "driver" || funding != "operating_balance" {
		driverError(c, http.StatusConflict, "charge_collection_not_supported", "Rider/platform charge collection must be implemented before this rule can be approved")
		return
	}
	if _, err = tx.Exec(c, `SELECT pg_advisory_xact_lock(hashtext($1))`, `statutory-rule:`+ledger); err != nil {
		reviewFailure(c)
		return
	}
	var overlap bool
	err = tx.QueryRow(c, `SELECT EXISTS(SELECT 1 FROM statutory_charge_rules p WHERE p.ledger_code=$1 AND p.status='approved' AND (p.effective_until IS NULL OR p.effective_until>$2) AND ($3::TIMESTAMPTZ IS NULL OR $3>p.effective_from))`, ledger, from, until).Scan(&overlap)
	if err != nil {
		reviewFailure(c)
		return
	}
	if overlap {
		driverError(c, 409, "statutory_rule_period_overlap", "A rule with this ledger code is already approved for an overlapping effective period")
		return
	}
	_, err = tx.Exec(c, `UPDATE statutory_charge_rules SET status='approved',approved_by=$2::UUID,approved_at=NOW() WHERE id=$1::UUID AND status='draft'`, id, currentAdmin(c).ID)
	if err != nil || adminAudit(c, tx, "statutory_charge_rule_approved", "", gin.H{"ruleId": id, "ledgerCode": ledger, "version": version}) != nil || tx.Commit(c) != nil {
		reviewFailure(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"id": id, "status": "approved"}})
}
func (a *adminAPI) requestStatutoryRuleRetirement(c *gin.Context) {
	id, ok := geofenceID(c, "id")
	if !ok {
		return
	}
	var b struct {
		EffectiveUntil time.Time `json:"effectiveUntil"`
	}
	if c.ShouldBindJSON(&b) != nil || b.EffectiveUntil.Before(time.Now()) {
		driverError(c, 400, "invalid_retirement", "A future effectiveUntil is required")
		return
	}
	tx, err := a.pool.Begin(c)
	if err != nil {
		reviewFailure(c)
		return
	}
	defer tx.Rollback(c)
	requestID := uuid.NewString()
	tag, err := tx.Exec(c, `INSERT INTO statutory_charge_rule_retirements(id,rule_id,effective_until,requested_by,status) SELECT $1::UUID,id,$3,$4::UUID,'pending' FROM statutory_charge_rules WHERE id=$2::UUID AND status='approved' AND (effective_until IS NULL OR effective_until>$3)`, requestID, id, b.EffectiveUntil.UTC(), currentAdmin(c).ID)
	if err != nil || tag.RowsAffected() != 1 {
		driverError(c, 409, "retirement_unavailable", "Statutory charge rule cannot be retired")
		return
	}
	if adminAudit(c, tx, "statutory_charge_rule_retirement_requested", "", gin.H{"ruleId": id, "requestId": requestID, "effectiveUntil": b.EffectiveUntil.UTC()}) != nil || tx.Commit(c) != nil {
		reviewFailure(c)
		return
	}
	c.JSON(201, contracts.APIResponse{Data: gin.H{"id": requestID, "status": "pending"}})
}
func (a *adminAPI) approveStatutoryRuleRetirement(c *gin.Context) {
	id, ok := geofenceID(c, "id")
	if !ok {
		return
	}
	rid, err := uuid.Parse(c.Param("requestID"))
	if err != nil {
		driverError(c, 400, "invalid_retirement_id", "Valid retirement request ID is required")
		return
	}
	tx, err := a.pool.Begin(c)
	if err != nil {
		reviewFailure(c)
		return
	}
	defer tx.Rollback(c)
	var ruleID, requester string
	var effective time.Time
	var status string
	err = tx.QueryRow(c, `SELECT rule_id::TEXT,requested_by::TEXT,effective_until,status FROM statutory_charge_rule_retirements WHERE id=$1::UUID FOR UPDATE`, rid.String()).Scan(&ruleID, &requester, &effective, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "retirement_not_found", "Retirement request was not found")
		return
	}
	if err != nil {
		reviewFailure(c)
		return
	}
	if ruleID != id || status != "pending" || requester == currentAdmin(c).ID {
		driverError(c, 409, "retirement_approval_unavailable", "A different staff member must approve this retirement request")
		return
	}
	_, err = tx.Exec(c, `UPDATE statutory_charge_rules SET effective_until=$2 WHERE id=$1::UUID AND status='approved' AND (effective_until IS NULL OR effective_until>$2)`, id, effective)
	if err == nil {
		_, err = tx.Exec(c, `UPDATE statutory_charge_rule_retirements SET status='approved',approved_by=$2::UUID,approved_at=NOW() WHERE id=$1::UUID`, rid.String(), currentAdmin(c).ID)
	}
	if err != nil || adminAudit(c, tx, "statutory_charge_rule_retired", "", gin.H{"ruleId": id, "requestId": rid.String(), "effectiveUntil": effective}) != nil || tx.Commit(c) != nil {
		reviewFailure(c)
		return
	}
	c.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id, "status": "retired", "effectiveUntil": effective}})
}
