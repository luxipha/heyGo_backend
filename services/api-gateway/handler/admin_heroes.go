package handler

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type heroesPolicy struct {
	Version    int64          `json:"version"`
	Enabled    bool           `json:"enabled"`
	TripTarget int            `json:"tripTarget"`
	RewardKobo int64          `json:"rewardKobo"`
	Ranks      map[string]int `json:"ranks"`
}

func validHeroesPolicy(p heroesPolicy) bool {
	if p.TripTarget < 1 || p.TripTarget > 10000 || p.RewardKobo < 0 || p.RewardKobo > 100000000 || p.Enabled && p.RewardKobo == 0 {
		return false
	}
	if len(p.Ranks) != 1 && len(p.Ranks) != 5 {
		return false
	}
	if n, ok := p.Ranks["E"]; !ok || n != 0 {
		return false
	}
	previous := 0
	for _, rank := range []string{"D", "C", "B", "A"} {
		n, ok := p.Ranks[rank]
		if len(p.Ranks) == 1 {
			continue
		}
		if !ok || n <= previous || n > 1000000 {
			return false
		}
		previous = n
	}
	return true
}
func (a *adminAPI) getHeroesPolicy(c *gin.Context) {
	var p heroesPolicy
	var ranks []byte
	err := a.pool.QueryRow(c.Request.Context(), `SELECT version,enabled,trip_target,reward_kobo,ranks FROM heroes_policies ORDER BY version DESC LIMIT 1`).Scan(&p.Version, &p.Enabled, &p.TripTarget, &p.RewardKobo, &ranks)
	if err != nil || json.Unmarshal(ranks, &p.Ranks) != nil {
		heroFailure(c)
		return
	}
	heroData(c, p)
}
func (a *adminAPI) saveHeroesPolicy(c *gin.Context) {
	var p heroesPolicy
	if c.ShouldBindJSON(&p) != nil || !validHeroesPolicy(p) {
		driverError(c, 400, "invalid_heroes_policy", "Set a valid reward, trip target and increasing E–A thresholds. E starts at zero.")
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
	if _, err = tx.Exec(c.Request.Context(), `INSERT INTO heroes_policies(id,enabled,trip_target,reward_kobo,ranks,created_by)VALUES($1::uuid,$2,$3,$4,$5::jsonb,$6::uuid)`, uuid.NewString(), p.Enabled, p.TripTarget, p.RewardKobo, string(ranks), currentAdmin(c).ID); err == nil {
		err = adminAudit(c, tx, "heroes.policy.created", "", gin.H{"previousVersion": current, "enabled": p.Enabled, "tripTarget": p.TripTarget, "rewardKobo": p.RewardKobo})
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
