package handler

import (
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/simulation"
	"math"
	"math/rand"
)

func (a *adminAPI) batchSimulationParticipants(c *gin.Context) {
	if !simulationID(c) {
		return
	}
	var b struct {
		Revision     int64                  `json:"revision"`
		Count        int                    `json:"count"`
		RadiusMeters float64                `json:"radiusMeters"`
		Participant  simulation.Participant `json:"participant"`
	}
	if !simulationBody(c, &b) || b.Count < 1 || b.Count > 1000 || math.IsNaN(b.RadiusMeters) || b.RadiusMeters < 0 || b.RadiusMeters > 10000 {
		driverError(c, 400, "invalid_batch", "Choose 1–1000 participants within 0–10000 metres")
		return
	}
	if e := b.Participant.Validate(); e != nil {
		driverError(c, 400, "invalid_participant", e.Error())
		return
	}
	ctx := c.Request.Context()
	tx, e := a.pool.Begin(ctx)
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Batch unavailable")
		return
	}
	defer tx.Rollback(ctx)
	var seed int64
	e = tx.QueryRow(ctx, `SELECT seed FROM simulation_runs WHERE id=$1::uuid AND status='draft' AND revision=$2 FOR UPDATE`, c.Param("runID"), b.Revision).Scan(&seed)
	if e != nil {
		driverError(c, 409, "simulation_changed", "Reload draft before adding a batch")
		return
	}
	var count int
	e = tx.QueryRow(ctx, `SELECT count(*) FROM simulation_participants WHERE simulation_run_id=$1::uuid AND role=$2`, c.Param("runID"), b.Participant.Role).Scan(&count)
	limit := 1000
	if b.Participant.Role == "rider" {
		limit = 100
	}
	if e != nil || count+b.Count > limit {
		driverError(c, 409, "population_limit", "Driver limit 1000; Rider limit 100")
		return
	}
	if b.Participant.ModelID != "" {
		var valid bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM vehicle_catalogue WHERE id=$1::uuid AND $2 BETWEEN year_from AND year_to)`, b.Participant.ModelID, b.Participant.ModelYear).Scan(&valid)
		if e != nil || !valid {
			driverError(c, 400, "invalid_model", "Choose a configured model/year")
			return
		}
	}
	for _, id := range b.Participant.CategoryIDs {
		var valid bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ride_categories WHERE id=$1::uuid)`, id).Scan(&valid)
		if e != nil || !valid {
			driverError(c, 400, "invalid_category", "Choose a configured category")
			return
		}
	}
	rng := rand.New(rand.NewSource(seed + b.Revision))
	for i := 0; i < b.Count; i++ {
		p := b.Participant
		p.Name = fmt.Sprintf("%.80s %d", p.Name, count+i+1)
		angle := rng.Float64() * 2 * math.Pi
		distance := math.Sqrt(rng.Float64()) * b.RadiusMeters
		p.Latitude += math.Cos(angle) * distance / 111320
		scale := math.Cos(p.Latitude * math.Pi / 180)
		if math.Abs(scale) < 0.01 {
			driverError(c, 400, "invalid_location", "Population placement too close to pole")
			return
		}
		p.Longitude += math.Sin(angle) * distance / (111320 * scale)
		if e = p.Validate(); e != nil {
			driverError(c, 400, "invalid_location", e.Error())
			return
		}
		raw, _ := json.Marshal(p)
		_, e = tx.Exec(ctx, `INSERT INTO simulation_participants(id,simulation_run_id,role,name,controller,configuration)VALUES($1::uuid,$2::uuid,$3,$4,$5,$6::jsonb)`, uuid.NewString(), c.Param("runID"), p.Role, p.Name, p.Controller, raw)
		if e != nil {
			driverError(c, 503, "simulation_unavailable", "Batch could not be saved")
			return
		}
	}
	_, e = tx.Exec(ctx, `UPDATE simulation_runs SET revision=revision+1,updated_at=NOW() WHERE id=$1::uuid`, c.Param("runID"))
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO simulation_events(simulation_run_id,kind,data)VALUES($1::uuid,'population.configured',$2::jsonb)`, c.Param("runID"), mustSimulationJSON(gin.H{"count": b.Count, "role": b.Participant.Role, "seed": seed, "radiusMeters": b.RadiusMeters, "revision": b.Revision}))
	}
	if e == nil {
		e = adminAudit(c, tx, "simulation.population.configured", "", gin.H{"simulationRunId": c.Param("runID"), "count": b.Count, "role": b.Participant.Role})
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Batch could not be saved")
		return
	}
	c.JSON(201, contracts.APIResponse{Data: gin.H{"count": b.Count, "revision": b.Revision + 1}})
}
func mustSimulationJSON(v any) []byte { b, _ := json.Marshal(v); return b }
