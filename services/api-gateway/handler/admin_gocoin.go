package handler

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
)

func validCoinPolicy(p coinPolicy) bool {
	if p.Version < 1 || p.CoinsPerBlock < 0 || p.CoinsPerBlock > 1000000 || p.CreditKoboPerBlock < 0 || p.CreditKoboPerBlock > 100000000 || len(p.Categories) > 100 {
		return false
	}
	if p.RedemptionEnabled && (p.CoinsPerBlock == 0 || p.CreditKoboPerBlock == 0) {
		return false
	}
	for slug, r := range p.Categories {
		if slug == "" || r.CoinsPerRide < 0 || r.CoinsPerRide > 1000000 || r.Enabled && r.CoinsPerRide == 0 {
			return false
		}
	}
	return true
}
func (a *adminAPI) getCoinPolicy(c *gin.Context) {
	p, e := readCoinPolicy(c, a.pool)
	if e != nil {
		heroFailure(c)
		return
	}
	heroData(c, p)
}
func (a *adminAPI) saveCoinPolicy(c *gin.Context) {
	var p coinPolicy
	if c.ShouldBindJSON(&p) != nil || !validCoinPolicy(p) {
		driverError(c, 400, "invalid_gocoin_policy", "Set integer category rewards and a positive conversion block before enabling redemption.")
		return
	}
	ctx := c.Request.Context()
	tx, e := a.pool.Begin(ctx)
	if e != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('gocoin-policy'))`); e != nil {
		heroFailure(c)
		return
	}
	current, e := readCoinPolicy(c, tx)
	if e != nil {
		heroFailure(c)
		return
	}
	if current.Version != p.Version {
		driverError(c, 409, "stale_gocoin_policy", "Reload policy before saving.")
		return
	}
	for slug := range p.Categories {
		var exists bool
		if tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ride_categories WHERE slug=$1)`, slug).Scan(&exists) != nil {
			heroFailure(c)
			return
		}
		if !exists {
			driverError(c, 400, "unknown_category", "Use catalogue category slugs.")
			return
		}
	}
	if p.Categories == nil {
		p.Categories = map[string]coinRule{}
	}
	raw, _ := json.Marshal(p.Categories)
	e = tx.QueryRow(ctx, `INSERT INTO gocoin_policies(earning_enabled,redemption_enabled,coins_per_block,credit_kobo_per_block,categories,created_by) VALUES($1,$2,$3,$4,$5::jsonb,$6::uuid) RETURNING version`, p.EarningEnabled, p.RedemptionEnabled, p.CoinsPerBlock, p.CreditKoboPerBlock, string(raw), currentAdmin(c).ID).Scan(&p.Version)
	if e == nil {
		e = adminAudit(c, tx, "gocoin.policy.created", "", p)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		heroFailure(c)
		return
	}
	heroData(c, p)
}
