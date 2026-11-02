package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"strings"
)

// Recovery terminates orphaned fixture activity, preserving every trip and financial record.
// It never marks unfinished trips completed/cancelled or reuses their initial conditions.
func (a *adminAPI) recoverSimulation(c *gin.Context) {
	if !simulationID(c) {
		return
	}
	var b struct {
		Revision int64 `json:"revision"`
	}
	if !simulationBody(c, &b) {
		driverError(c, 400, "invalid_run_action", "Current revision required")
		return
	}
	ctx := c.Request.Context()
	tx, e := a.pool.Begin(ctx)
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Recovery unavailable")
		return
	}
	defer tx.Rollback(ctx)
	var acquired bool
	e = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, "simulation:"+c.Param("runID")).Scan(&acquired)
	if e != nil || !acquired {
		driverError(c, 409, "simulation_runtime_active", "Use Stop while the runtime lease is active")
		return
	}
	result, e := tx.Exec(ctx, `UPDATE simulation_runs SET status='stopped',revision=revision+1,updated_at=NOW() WHERE id=$1::uuid AND revision=$2 AND status IN ('starting','running','paused','stopping','failed')`, c.Param("runID"), b.Revision)
	if e != nil || result.RowsAffected() != 1 {
		driverError(c, 409, "simulation_changed", "Refresh recoverable run")
		return
	}
	for _, q := range []string{
		`UPDATE user_sessions s SET revoked_at=COALESCE(revoked_at,NOW()) WHERE s.device_name='Simulation runtime' AND EXISTS(SELECT 1 FROM simulation_participants p WHERE p.simulation_run_id=$1::uuid AND p.actor_user_id=s.user_id)`,
		`UPDATE driver_socket_sessions s SET expires_at=LEAST(expires_at,NOW()) WHERE EXISTS(SELECT 1 FROM simulation_participants p WHERE p.simulation_run_id=$1::uuid AND p.actor_user_id=s.driver_id)`,
		`UPDATE drivers d SET available=FALSE,online_requested=FALSE,updated_at=NOW() WHERE EXISTS(SELECT 1 FROM simulation_participants p WHERE p.simulation_run_id=$1::uuid AND p.actor_user_id=d.id)`,
		`UPDATE simulation_participants SET runtime_status='recovered_stopped',updated_at=NOW() WHERE simulation_run_id=$1::uuid`,
		`UPDATE simulation_commands SET status='failed',processed_at=NOW() WHERE simulation_run_id=$1::uuid AND status='queued'`,
		`UPDATE simulation_rider_actions SET status='failed',error='orphaned runtime recovered; inspect trip state before retrying a booking',processed_at=NOW() WHERE simulation_run_id=$1::uuid AND status IN ('queued','processing')`,
	} {
		if _, e = tx.Exec(ctx, q, c.Param("runID")); e != nil {
			driverError(c, 503, "simulation_unavailable", "Recovery could not be committed")
			return
		}
	}
	_, e = tx.Exec(ctx, `INSERT INTO simulation_events(simulation_run_id,kind,data)VALUES($1::uuid,'run.recovered','{"unfinishedTripsPreserved":true,"sessionsRevoked":true,"supplyDisabled":true}'::jsonb)`, c.Param("runID"))
	if e == nil {
		e = adminAudit(c, tx, "simulation.run.recovered", "", gin.H{"simulationRunId": c.Param("runID")})
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Recovery unavailable")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"status": "stopped", "revision": b.Revision + 1}})
}
func (a *adminAPI) cloneSimulation(c *gin.Context) {
	if !simulationID(c) {
		return
	}
	var b struct {
		Name     string `json:"name"`
		Revision int64  `json:"revision"`
	}
	if !simulationBody(c, &b) || strings.TrimSpace(b.Name) == "" || len(b.Name) > 120 {
		driverError(c, 400, "invalid_run", "New run name required")
		return
	}
	ctx := c.Request.Context()
	tx, e := a.pool.Begin(ctx)
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Clone unavailable")
		return
	}
	defer tx.Rollback(ctx)
	id := uuid.NewString()
	result, e := tx.Exec(ctx, `INSERT INTO simulation_runs(id,name,seed,created_by,request_concurrency)SELECT $3::uuid,$4,seed,$5::uuid,request_concurrency FROM simulation_runs WHERE id=$1::uuid AND revision=$2`, c.Param("runID"), b.Revision, id, strings.TrimSpace(b.Name), currentAdmin(c).ID)
	if e != nil || result.RowsAffected() != 1 {
		driverError(c, 409, "simulation_changed", "Refresh source run")
		return
	}
	_, e = tx.Exec(ctx, `INSERT INTO simulation_participants(id,simulation_run_id,role,name,controller,configuration)SELECT gen_random_uuid(),$2::uuid,role,name,controller,configuration FROM simulation_participants WHERE simulation_run_id=$1::uuid ORDER BY created_at,id`, c.Param("runID"), id)
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO simulation_events(simulation_run_id,kind,data)VALUES($1::uuid,'run.cloned',jsonb_build_object('sourceRunId',$2::text,'sourceRevision',$3::bigint))`, id, c.Param("runID"), b.Revision)
	}
	if e == nil {
		e = adminAudit(c, tx, "simulation.run.cloned", "", gin.H{"simulationRunId": id, "sourceRunId": c.Param("runID")})
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Clone unavailable")
		return
	}
	c.JSON(201, contracts.APIResponse{Data: gin.H{"id": id, "status": "draft", "revision": 1}})
}
