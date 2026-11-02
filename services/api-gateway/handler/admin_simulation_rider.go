package handler

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/simulation"
)

func (a *adminAPI) queueSimulationRider(c *gin.Context) {
	if !simulationID(c) {
		return
	}
	if _, e := uuid.Parse(c.Param("participantID")); e != nil {
		driverError(c, 400, "invalid_participant", "Valid participant required")
		return
	}
	var b simulation.RiderAction
	if !simulationBody(c, &b) {
		driverError(c, 400, "invalid_rider_action", "Valid Rider action required")
		return
	}
	if e := b.Validate(); e != nil {
		driverError(c, 400, "invalid_rider_action", e.Error())
		return
	}
	ctx := c.Request.Context()
	tx, e := a.pool.Begin(ctx)
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Rider unavailable")
		return
	}
	defer tx.Rollback(ctx)
	var owned bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM simulation_participants p JOIN simulation_runs r ON r.id=p.simulation_run_id WHERE p.id=$1::uuid AND p.simulation_run_id=$2::uuid AND p.is_simulated AND p.role=$3 AND p.controller='simulator' AND p.actor_user_id IS NOT NULL AND r.status='running')`, c.Param("participantID"), c.Param("runID"), b.OwnerRole()).Scan(&owned)
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Rider unavailable")
		return
	}
	if !owned {
		driverError(c, 409, "rider_not_running", "Choose a provisioned Rider in a running run")
		return
	}
	raw, _ := json.Marshal(b)
	// Serialize the actor's actions. Replays of the same command remain readable and never create another booking.
	var actor string
	e = tx.QueryRow(ctx, `SELECT id::text FROM simulation_participants WHERE id=$1::uuid FOR UPDATE`, c.Param("participantID")).Scan(&actor)
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Rider unavailable")
		return
	}
	var conflict bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM simulation_rider_actions WHERE id=$1::uuid AND NOT(simulation_run_id=$2::uuid AND participant_id=$3::uuid AND payload=$4::jsonb))`, b.CommandID, c.Param("runID"), actor, raw).Scan(&conflict)
	if e != nil || conflict {
		driverError(c, 409, "command_id_conflict", "Command identity conflict")
		return
	}
	var pending bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM simulation_rider_actions WHERE participant_id=$1::uuid AND status IN ('queued','processing') AND id<>$2::uuid)`, actor, b.CommandID).Scan(&pending)
	if e != nil || pending {
		driverError(c, 409, "rider_action_pending", "Wait for the current Rider action")
		return
	}
	_, e = tx.Exec(ctx, `INSERT INTO simulation_rider_actions(id,simulation_run_id,participant_id,action,payload,scheduled_at)VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::jsonb,NOW()+make_interval(secs=>$6))ON CONFLICT(id)DO NOTHING`, b.CommandID, c.Param("runID"), actor, b.Action, raw, b.DelaySeconds)
	if e == nil {
		e = adminAudit(c, tx, "simulation.rider.action_queued", "", gin.H{"simulationRunId": c.Param("runID"), "simulationParticipantId": actor, "commandId": b.CommandID, "action": b.Action})
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Cannot queue Rider action")
		return
	}
	c.JSON(202, contracts.APIResponse{Data: gin.H{"commandId": b.CommandID, "status": "queued"}})
}
