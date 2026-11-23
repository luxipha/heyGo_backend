package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type heroesPolicy struct {
	RankTripRequirements map[string]int `json:"rankTripRequirements"`
	RankMinimumTrips     int            `json:"rankMinimumTrips"`
	Version              int64          `json:"version"`
	Enabled              bool           `json:"enabled"`
	TripTarget           int            `json:"tripTarget"`
	RewardKobo           int64          `json:"rewardKobo"`
	Ranks                map[string]int `json:"ranks"`
}

func validHeroesPolicy(p heroesPolicy) bool {
	if p.RankMinimumTrips < 0 || p.RankMinimumTrips > 10000 || p.TripTarget < 1 || p.TripTarget > 10000 || p.RewardKobo < 0 || p.RewardKobo > 100000000 || p.Enabled && p.RewardKobo == 0 {
		return false
	}
	if len(p.Ranks) != 1 && len(p.Ranks) != 5 {
		return false
	}
	if n, ok := p.Ranks["E"]; !ok || n != 0 {
		return false
	}
	requirements := effectiveRankTripRequirements(p)
	if len(requirements) != len(p.Ranks) || requirements["E"] != 0 {
		return false
	}
	for rank, trips := range requirements {
		if _, ok := p.Ranks[rank]; !ok || trips < 0 || trips > 10000 {
			return false
		}
	}
	previousTrips := 0
	previous := 0
	for _, rank := range []string{"D", "C", "B", "A"} {
		n, ok := p.Ranks[rank]
		if len(p.Ranks) == 1 {
			continue
		}
		trips, hasTrips := requirements[rank]
		if !ok || !hasTrips || trips < previousTrips || n <= previous || n > 1000000 {
			return false
		}
		previous = n
		previousTrips = trips
	}
	return true
}
func (a *adminAPI) getHeroesPolicy(c *gin.Context) {
	var p heroesPolicy
	var ranks, requirements []byte
	err := a.pool.QueryRow(c.Request.Context(), `SELECT version,enabled,trip_target,reward_kobo,ranks,rank_minimum_trips,rank_trip_requirements FROM heroes_policies ORDER BY version DESC LIMIT 1`).Scan(&p.Version, &p.Enabled, &p.TripTarget, &p.RewardKobo, &ranks, &p.RankMinimumTrips, &requirements)
	if err != nil || json.Unmarshal(ranks, &p.Ranks) != nil {
		heroFailure(c)
		return
	}
	if len(requirements) > 0 && string(requirements) != "null" {
		if json.Unmarshal(requirements, &p.RankTripRequirements) != nil {
			heroFailure(c)
			return
		}
	}
	p.RankTripRequirements = effectiveRankTripRequirements(p)
	heroData(c, p)
}
func (a *adminAPI) saveHeroesPolicy(c *gin.Context) {
	var p heroesPolicy
	if c.ShouldBindJSON(&p) != nil || !validHeroesPolicy(p) {
		driverError(c, 400, "invalid_heroes_policy", "Set a valid reward, trip target and increasing E–A thresholds, and a separate trip requirement (0–10,000) for each rank. Driver counts must increase and trip requirements cannot decrease from E to A. E starts at zero.")
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	if _, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(hashtext('heroes-policy'))`); err != nil {
		heroFailure(c)
		return
	}
	var current int64
	if tx.QueryRow(c.Request.Context(), `SELECT version FROM heroes_policies ORDER BY version DESC LIMIT 1`).Scan(&current) != nil {
		heroFailure(c)
		return
	}
	if p.Version != current {
		driverError(c, 409, "stale_heroes_policy", "Settings changed. Reload before saving.")
		return
	}
	ranks, _ := json.Marshal(p.Ranks)
	requirements, _ := json.Marshal(effectiveRankTripRequirements(p))
	if _, err = tx.Exec(c.Request.Context(), `INSERT INTO heroes_policies(id,enabled,trip_target,reward_kobo,ranks,created_by,rank_minimum_trips,rank_trip_requirements)VALUES($1::uuid,$2,$3,$4,$5::jsonb,$6::uuid,$7,$8::jsonb)`, uuid.NewString(), p.Enabled, p.TripTarget, p.RewardKobo, string(ranks), currentAdmin(c).ID, p.RankMinimumTrips, string(requirements)); err == nil {
		err = adminAudit(c, tx, "heroes.policy.created", "", gin.H{"rankTripRequirements": effectiveRankTripRequirements(p), "rankMinimumTrips": p.RankMinimumTrips, "previousVersion": current, "enabled": p.Enabled, "tripTarget": p.TripTarget, "rewardKobo": p.RewardKobo})
	}
	if err == nil {
		err = tx.Commit(c.Request.Context())
	}
	if err != nil {
		heroFailure(c)
		return
	}
	a.getHeroesPolicy(c)
}

// Legacy clients/policies retain their original minimum until saved with rank-specific requirements.
func effectiveRankTripRequirements(p heroesPolicy) map[string]int {
	if p.RankTripRequirements != nil {
		return p.RankTripRequirements
	}
	requirements := make(map[string]int, len(p.Ranks))
	for rank := range p.Ranks {
		if rank == "E" {
			requirements[rank] = 0
		} else {
			requirements[rank] = p.RankMinimumTrips
		}
	}
	return requirements
}

type heroesSettings struct {
	AcceptingNew     bool  `json:"acceptingNew"`
	LiveBonusKobo    int64 `json:"liveBonusKobo"`
	OrdersBonusKobo  int64 `json:"ordersBonusKobo"`
	OrdersBonusCount int   `json:"ordersBonusCount"`
	ReviewHoursMin   int   `json:"reviewHoursMin"`
	ReviewHoursMax   int   `json:"reviewHoursMax"`
}

func readHeroesSettings(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (heroesSettings, error) {
	s := heroesSettings{AcceptingNew: true, LiveBonusKobo: 50000, OrdersBonusKobo: 50000, OrdersBonusCount: 5, ReviewHoursMin: 5, ReviewHoursMax: 48}
	err := q.QueryRow(ctx, `SELECT accepting_new,live_bonus_kobo,orders_bonus_kobo,orders_bonus_count,review_hours_min,review_hours_max FROM heroes_settings WHERE id=1`).Scan(&s.AcceptingNew, &s.LiveBonusKobo, &s.OrdersBonusKobo, &s.OrdersBonusCount, &s.ReviewHoursMin, &s.ReviewHoursMax)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	return s, err
}

func validHeroesSettings(s heroesSettings) bool {
	return s.LiveBonusKobo >= 0 && s.LiveBonusKobo <= 10000000 &&
		s.OrdersBonusKobo >= 0 && s.OrdersBonusKobo <= 10000000 &&
		s.OrdersBonusCount >= 1 && s.OrdersBonusCount <= 1000 &&
		s.ReviewHoursMin >= 0 && s.ReviewHoursMax <= 720 && s.ReviewHoursMin <= s.ReviewHoursMax
}

func (a *adminAPI) getHeroesSettings(c *gin.Context) {
	s, err := readHeroesSettings(c.Request.Context(), a.pool)
	if err != nil {
		heroFailure(c)
		return
	}
	heroData(c, s)
}

func (a *adminAPI) saveHeroesSettings(c *gin.Context) {
	var b struct {
		AcceptingNew     *bool  `json:"acceptingNew"`
		LiveBonusKobo    *int64 `json:"liveBonusKobo"`
		OrdersBonusKobo  *int64 `json:"ordersBonusKobo"`
		OrdersBonusCount *int   `json:"ordersBonusCount"`
		ReviewHoursMin   *int   `json:"reviewHoursMin"`
		ReviewHoursMax   *int   `json:"reviewHoursMax"`
	}
	if c.ShouldBindJSON(&b) != nil {
		driverError(c, 400, "invalid_settings", "Check the Heroes settings and try again")
		return
	}
	ctx := c.Request.Context()
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		heroFailure(c)
		return
	}
	defer tx.Rollback(ctx)
	// Fields left out keep their current value, so the sign-up switch can be saved alone.
	next, err := readHeroesSettings(ctx, tx)
	if err != nil {
		heroFailure(c)
		return
	}
	if b.AcceptingNew != nil {
		next.AcceptingNew = *b.AcceptingNew
	}
	if b.LiveBonusKobo != nil {
		next.LiveBonusKobo = *b.LiveBonusKobo
	}
	if b.OrdersBonusKobo != nil {
		next.OrdersBonusKobo = *b.OrdersBonusKobo
	}
	if b.OrdersBonusCount != nil {
		next.OrdersBonusCount = *b.OrdersBonusCount
	}
	if b.ReviewHoursMin != nil {
		next.ReviewHoursMin = *b.ReviewHoursMin
	}
	if b.ReviewHoursMax != nil {
		next.ReviewHoursMax = *b.ReviewHoursMax
	}
	if !validHeroesSettings(next) {
		driverError(c, 400, "invalid_settings", "Bonuses can be up to ₦100,000, the order count 1 to 1,000, and review hours 0 to 720 with the minimum not above the maximum")
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO heroes_settings(id,accepting_new,live_bonus_kobo,orders_bonus_kobo,orders_bonus_count,review_hours_min,review_hours_max,updated_by,updated_at)VALUES(1,$1,$2,$3,$4,$5,$6,$7::uuid,now())
ON CONFLICT(id)DO UPDATE SET accepting_new=EXCLUDED.accepting_new,live_bonus_kobo=EXCLUDED.live_bonus_kobo,orders_bonus_kobo=EXCLUDED.orders_bonus_kobo,orders_bonus_count=EXCLUDED.orders_bonus_count,review_hours_min=EXCLUDED.review_hours_min,review_hours_max=EXCLUDED.review_hours_max,updated_by=EXCLUDED.updated_by,updated_at=now()`,
		next.AcceptingNew, next.LiveBonusKobo, next.OrdersBonusKobo, next.OrdersBonusCount, next.ReviewHoursMin, next.ReviewHoursMax, currentAdmin(c).ID); err == nil {
		err = adminAudit(c, tx, "heroes.settings.updated", "", gin.H{"acceptingNew": next.AcceptingNew, "liveBonusKobo": next.LiveBonusKobo, "ordersBonusKobo": next.OrdersBonusKobo, "ordersBonusCount": next.OrdersBonusCount, "reviewHoursMin": next.ReviewHoursMin, "reviewHoursMax": next.ReviewHoursMax})
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		heroFailure(c)
		return
	}
	heroData(c, next)
}
