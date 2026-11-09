package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"log/slog"
	"strconv"
	"time"
)

type coinRule struct {
	Enabled      bool  `json:"enabled"`
	CoinsPerRide int64 `json:"coinsPerRide"`
}
type coinPolicy struct {
	Version            int64               `json:"version"`
	EarningEnabled     bool                `json:"earningEnabled"`
	RedemptionEnabled  bool                `json:"redemptionEnabled"`
	CoinsPerBlock      int64               `json:"coinsPerBlock"`
	CreditKoboPerBlock int64               `json:"creditKoboPerBlock"`
	Categories         map[string]coinRule `json:"categories"`
}

func readCoinPolicy(c *gin.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}) (coinPolicy, error) {
	var p coinPolicy
	var raw []byte
	e := q.QueryRow(c.Request.Context(), `SELECT version,earning_enabled,redemption_enabled,coins_per_block,credit_kobo_per_block,categories FROM gocoin_policies ORDER BY version DESC LIMIT 1`).Scan(&p.Version, &p.EarningEnabled, &p.RedemptionEnabled, &p.CoinsPerBlock, &p.CreditKoboPerBlock, &raw)
	if e == nil {
		e = json.Unmarshal(raw, &p.Categories)
	}
	return p, e
}
func registerRiderWalletRoutes(group *gin.RouterGroup, pool *pgxpool.Pool) {
	r := group.Group("/rider", gatewayauth.RequireRole("rider"))
	r.GET("/wallet", func(c *gin.Context) {
		u, _ := gatewayauth.CurrentUser(c)
		var coins, credit int64
		e := pool.QueryRow(c.Request.Context(), `SELECT COALESCE((SELECT balance FROM gocoin_accounts WHERE rider_id=$1::uuid),0),COALESCE((SELECT balance_kobo FROM rider_credit_accounts WHERE rider_id=$1::uuid),0)`, u.ID).Scan(&coins, &credit)
		p, pe := readCoinPolicy(c, pool)
		if e != nil || pe != nil {
			heroFailure(c)
			return
		}
		var equivalent int64
		if p.CoinsPerBlock > 0 {
			equivalent = (coins / p.CoinsPerBlock) * p.CreditKoboPerBlock
		}
		c.Header("Cache-Control", "no-store")
		heroData(c, gin.H{"goCoins": coins, "rideCreditKobo": credit, "currency": "NGN", "withdrawable": false, "redeemableCreditKobo": equivalent, "policy": p})
	})
	r.POST("/gocoin/redeem", func(c *gin.Context) { redeemCoins(c, pool) })
	r.GET("/gocoin/transactions", func(c *gin.Context) { walletHistory(c, pool, true) })
	r.GET("/credits/transactions", func(c *gin.Context) { walletHistory(c, pool, false) })
}
func walletHistory(c *gin.Context, pool *pgxpool.Pool, coins bool) {
	u, _ := gatewayauth.CurrentUser(c)
	limit := 50
	if s := c.Query("limit"); s != "" {
		n, e := strconv.Atoi(s)
		if e != nil || n < 1 || n > 100 {
			driverError(c, 400, "invalid_limit", "Limit must be 1–100.")
			return
		}
		limit = n
	}
	before := time.Now().UTC().Add(time.Second)
	id := "ffffffff-ffff-ffff-ffff-ffffffffffff"
	if s := c.Query("before"); s != "" {
		var e error
		before, e = time.Parse(time.RFC3339Nano, s)
		id = c.Query("beforeId")
		if e != nil || uuid.Validate(id) != nil {
			driverError(c, 400, "invalid_cursor", "Use both cursor fields returned by the previous page.")
			return
		}
	}
	query := `SELECT id::text,delta,balance_after,kind,COALESCE(trip_id::text,''),created_at FROM gocoin_entries WHERE rider_id=$1::uuid AND (created_at,id)<($2,$3::uuid) ORDER BY created_at DESC,id DESC LIMIT $4`
	if !coins {
		query = `SELECT id::text,delta_kobo,balance_after_kobo,kind,COALESCE(trip_id::text,''),created_at FROM rider_credit_entries WHERE rider_id=$1::uuid AND (created_at,id)<($2,$3::uuid) ORDER BY created_at DESC,id DESC LIMIT $4`
	}
	rows, e := pool.Query(c.Request.Context(), query, u.ID, before, id, limit+1)
	if e != nil {
		heroFailure(c)
		return
	}
	defer rows.Close()
	type entry struct {
		ID           string    `json:"id"`
		Delta        int64     `json:"delta"`
		BalanceAfter int64     `json:"balanceAfter"`
		Kind         string    `json:"kind"`
		TripID       string    `json:"tripId,omitempty"`
		CreatedAt    time.Time `json:"createdAt"`
	}
	items := []entry{}
	for rows.Next() {
		var v entry
		if rows.Scan(&v.ID, &v.Delta, &v.BalanceAfter, &v.Kind, &v.TripID, &v.CreatedAt) != nil {
			heroFailure(c)
			return
		}
		items = append(items, v)
	}
	if rows.Err() != nil {
		heroFailure(c)
		return
	}
	var cursor any
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		cursor = gin.H{"before": last.CreatedAt.Format(time.RFC3339Nano), "beforeId": last.ID}
	}
	unit := "coins"
	if !coins {
		unit = "kobo"
	}
	heroData(c, gin.H{"items": items, "nextCursor": cursor, "unit": unit})
}
func redeemCoins(c *gin.Context, pool *pgxpool.Pool) {
	var b struct {
		RequestID     string `json:"requestId"`
		Coins         int64  `json:"coins"`
		PolicyVersion int64  `json:"policyVersion"`
	}
	if c.ShouldBindJSON(&b) != nil || uuid.Validate(b.RequestID) != nil || b.Coins < 1 || b.Coins > 1000000 || b.PolicyVersion < 1 {
		driverError(c, 400, "invalid_redemption", "Supply a request UUID, policy version and 1–1,000,000 coins.")
		return
	}
	ctx := c.Request.Context()
	u, _ := gatewayauth.CurrentUser(c)
	tx, e := pool.Begin(ctx)
	if e != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(ctx)
	// Shared order with referral approvals and ride-credit spending.
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('referral-approval-batch'))`); e != nil {
		heroFailure(c)
		return
	}
	var oldCoins, credit, version int64
	e = tx.QueryRow(ctx, `SELECT coins,credit_kobo,policy_version FROM gocoin_redemptions WHERE rider_id=$1::uuid AND request_id=$2::uuid`, u.ID, b.RequestID).Scan(&oldCoins, &credit, &version)
	if e == nil {
		if oldCoins != b.Coins || version != b.PolicyVersion {
			driverError(c, 409, "redemption_conflict", "Request ID was already used for different redemption details.")
			return
		}
		heroData(c, gin.H{"requestId": b.RequestID, "coins": oldCoins, "creditKobo": credit, "policyVersion": version})
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		heroFailure(c)
		return
	}
	p, e := readCoinPolicy(c, tx)
	if e != nil {
		heroFailure(c)
		return
	}
	if !p.RedemptionEnabled || p.Version != b.PolicyVersion || p.CoinsPerBlock < 1 || b.Coins%p.CoinsPerBlock != 0 {
		driverError(c, 409, "redemption_unavailable", "Reload the current policy. Redemption must be enabled and use whole conversion blocks.")
		return
	}
	credit = (b.Coins / p.CoinsPerBlock) * p.CreditKoboPerBlock
	tag, e := tx.Exec(ctx, `UPDATE gocoin_accounts SET balance=balance-$2 WHERE rider_id=$1::uuid AND balance>=$2`, u.ID, b.Coins)
	if e != nil {
		heroFailure(c)
		return
	}
	if tag.RowsAffected() != 1 {
		driverError(c, 409, "insufficient_coins", "Not enough GoCoins.")
		return
	}
	source := "gocoin:" + u.ID + ":" + b.RequestID
	_, e = tx.Exec(ctx, `INSERT INTO rider_credit_accounts(rider_id) VALUES($1::uuid) ON CONFLICT DO NOTHING`, u.ID)
	if e == nil {
		_, e = tx.Exec(ctx, `UPDATE rider_credit_accounts SET balance_kobo=balance_kobo+$2,updated_at=NOW() WHERE rider_id=$1::uuid`, u.ID, credit)
	}
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO gocoin_redemptions(rider_id,request_id,coins,credit_kobo,policy_version) VALUES($1::uuid,$2::uuid,$3,$4,$5)`, u.ID, b.RequestID, b.Coins, credit, p.Version)
	}
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO gocoin_entries(id,rider_id,source_key,delta,balance_after,kind,policy_version) SELECT $1::uuid,rider_id,$3,-($4::bigint),balance,'redemption',$5 FROM gocoin_accounts WHERE rider_id=$2::uuid`, uuid.NewString(), u.ID, source, b.Coins, p.Version)
	}
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO rider_credit_entries(id,rider_id,source_key,delta_kobo,balance_after_kobo,kind) SELECT $1::uuid,rider_id,$3,$4,balance_kobo,'gocoin_redemption' FROM rider_credit_accounts WHERE rider_id=$2::uuid`, uuid.NewString(), u.ID, source, credit)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		slog.Warn("GoCoin redemption transaction failed", "error", e)
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"requestId": b.RequestID, "coins": b.Coins, "creditKobo": credit, "policyVersion": p.Version})
}
