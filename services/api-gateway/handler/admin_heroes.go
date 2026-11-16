package handler

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
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
