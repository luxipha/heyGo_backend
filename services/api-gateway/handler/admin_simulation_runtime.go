package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/simulation"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func (a *adminAPI) simulationRuntime() *simulation.Runtime {
	base := strings.TrimSpace(os.Getenv("SIMULATION_API_URL"))
	if base == "" {
		base = "http://127.0.0.1:8080"
	}
	origin := strings.TrimSpace(os.Getenv("SIMULATION_WS_ORIGIN"))
	if origin == "" {
		origin = strings.TrimSpace(strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",")[0])
	}
	return &simulation.Runtime{Pool: a.pool, BaseURL: base, Origin: origin}
}
func (a *adminAPI) startSimulation(c *gin.Context) {
	if !simulationID(c) {
		return
	}
	var body struct {
		Revision           int64 `json:"revision"`
		RequestConcurrency int   `json:"requestConcurrency"`
	}
	if !simulationBody(c, &body) {
		driverError(c, 400, "invalid_run_action", "Current revision required")
		return
	}
	if body.RequestConcurrency == 0 {
		body.RequestConcurrency = 8
	}
	if body.RequestConcurrency < 1 || body.RequestConcurrency > 100 {
		driverError(c, 400, "invalid_concurrency", "Choose 1–100 concurrent simulated client calls")
		return
	}
	runtime := a.simulationRuntime()
	u, e := url.Parse(runtime.BaseURL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || (u.Scheme != "http" && u.Scheme != "https") || runtime.Origin == "" {
		driverError(c, 503, "simulation_runtime_unconfigured", "Runtime API URL and allowed socket origin required")
		return
	}
	ctx := c.Request.Context()
	tx, e := a.pool.Begin(ctx)
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Runtime unavailable")
		return
	}
	defer tx.Rollback(ctx)
	var count, apps int
	e = tx.QueryRow(ctx, `SELECT count(*),count(*)FILTER(WHERE controller='app')FROM simulation_participants WHERE simulation_run_id=$1::uuid`, c.Param("runID")).Scan(&count, &apps)
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Cannot read population")
		return
	}
	if count < 1 || count > 1100 {
		driverError(c, 409, "simulation_population_limit", "Configure up to 1000 Drivers and 100 Riders per run")
		return
	}
	if apps > 0 {
		driverError(c, 409, "app_handoff_pending", "App-controlled participant handoff is not connected; use simulator-controlled actors for this runtime")
		return
	}
	result, e := tx.Exec(ctx, `UPDATE simulation_runs SET status='starting',request_concurrency=$3,revision=revision+1,updated_at=NOW()WHERE id=$1::uuid AND is_simulated AND status='draft' AND revision=$2`, c.Param("runID"), body.Revision, body.RequestConcurrency)
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Cannot start run")
		return
	}
	if result.RowsAffected() != 1 {
		driverError(c, 409, "simulation_changed", "Reload the draft run; an already provisioned run is not reset automatically")
		return
	}
	e = adminAudit(c, tx, "simulation.run.start_requested", "", gin.H{"simulationRunId": c.Param("runID")})
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Cannot start run")
		return
	}
	go runtime.Run(c.Param("runID"))
	c.JSON(http.StatusAccepted, contracts.APIResponse{Data: gin.H{"status": "starting", "maximumSeconds": 600, "revision": body.Revision + 1}})
}
func (a *adminAPI) stopSimulation(c *gin.Context) {
	if !simulationID(c) {
		return
	}
	var body struct {
		Revision int64 `json:"revision"`
	}
	if !simulationBody(c, &body) {
		driverError(c, 400, "invalid_run_action", "Current revision required")
		return
	}
	ctx := c.Request.Context()
	tx, e := a.pool.Begin(ctx)
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Runtime unavailable")
		return
	}
	defer tx.Rollback(ctx)
	var noWorker bool
	if e = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, "simulation:"+c.Param("runID")).Scan(&noWorker); e != nil {
		driverError(c, 503, "simulation_unavailable", "Runtime ownership unavailable")
		return
	}
	if noWorker {
		driverError(c, 409, "simulation_runtime_disconnected", "No active runtime lease. Refresh if just starting; after a worker restart the preserved run requires recovery.")
		return
	}
	result, e := tx.Exec(ctx, `UPDATE simulation_runs SET status='stopping',revision=revision+1,updated_at=NOW()WHERE id=$1::uuid AND is_simulated AND status IN ('starting','running','paused')AND revision=$2`, c.Param("runID"), body.Revision)
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Stop unavailable")
		return
	}
	if result.RowsAffected() != 1 {
		driverError(c, 409, "simulation_changed", "Reload the active run before stopping")
		return
	}
	e = adminAudit(c, tx, "simulation.run.stop_requested", "", gin.H{"simulationRunId": c.Param("runID")})
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Stop unavailable")
		return
	}
	c.JSON(202, contracts.APIResponse{Data: gin.H{"status": "stopping", "revision": body.Revision + 1}})
}
func (a *adminAPI) respondSimulationOffer(c *gin.Context) {
	if !simulationID(c) {
		return
	}
	var body struct {
		CommandID string `json:"commandId"`
		TripID    string `json:"tripId"`
		Action    string `json:"action"`
	}
	if !simulationBody(c, &body) {
		driverError(c, 400, "invalid_offer_response", "Valid command required")
		return
	}
	for _, id := range []string{body.CommandID, body.TripID, c.Param("participantID")} {
		if _, e := uuid.Parse(id); e != nil {
			driverError(c, 400, "invalid_offer_response", "Valid command, participant and trip IDs required")
			return
		}
	}
	if body.Action != "accept" && body.Action != "decline" {
		driverError(c, 400, "invalid_offer_response", "Choose accept or decline")
		return
	}
	ctx := c.Request.Context()
	tx, e := a.pool.Begin(ctx)
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Offer response unavailable")
		return
	}
	defer tx.Rollback(ctx)
	var owned bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM simulation_participants p JOIN simulation_runs r ON r.id=p.simulation_run_id JOIN driver_assignments a ON a.driver_id=p.actor_user_id AND a.trip_id=$3::uuid WHERE p.id=$1::uuid AND p.simulation_run_id=$2::uuid AND p.controller='simulator' AND p.is_simulated AND r.status='running'AND a.status='offered'AND a.expires_at>NOW())`, c.Param("participantID"), c.Param("runID"), body.TripID).Scan(&owned)
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Offer response unavailable")
		return
	}
	if !owned {
		driverError(c, 409, "offer_not_current", "This participant does not own a current offer in the running simulation")
		return
	}
	result, e := tx.Exec(ctx, `INSERT INTO simulation_commands(id,simulation_run_id,participant_id,trip_id,action)VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5)ON CONFLICT(id)DO NOTHING`, body.CommandID, c.Param("runID"), c.Param("participantID"), body.TripID, body.Action)
	if e == nil && result.RowsAffected() == 0 {
		var matches bool
		e = tx.QueryRow(ctx, `SELECT simulation_run_id=$2::uuid AND participant_id=$3::uuid AND trip_id=$4::uuid AND action=$5 FROM simulation_commands WHERE id=$1::uuid`, body.CommandID, c.Param("runID"), c.Param("participantID"), body.TripID, body.Action).Scan(&matches)
		if e != nil || !matches {
			driverError(c, 409, "command_id_conflict", "Command ID is already in use")
			return
		}
	}
	if e == nil {
		e = adminAudit(c, tx, "simulation.offer.response_queued", "", gin.H{"simulationParticipantId": c.Param("participantID"), "simulationRunId": c.Param("runID"), "tripId": body.TripID, "action": body.Action, "commandId": body.CommandID})
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Offer response unavailable")
		return
	}
	c.JSON(202, contracts.APIResponse{Data: gin.H{"commandId": body.CommandID, "status": "queued"}})
}

// Pause affects simulator movement and queued actions only; real offer/wait clocks keep running.
func (a *adminAPI) pauseSimulation(c *gin.Context) {
	if !simulationID(c) {
		return
	}
	var b struct {
		Revision int64 `json:"revision"`
		Paused   bool  `json:"paused"`
	}
	if !simulationBody(c, &b) {
		driverError(c, 400, "invalid_run_action", "Revision and pause state required")
		return
	}
	from, to := "running", "paused"
	if !b.Paused {
		from, to = "paused", "running"
	}
	ctx := c.Request.Context()
	tx, e := a.pool.Begin(ctx)
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Pause unavailable")
		return
	}
	defer tx.Rollback(ctx)
	result, e := tx.Exec(ctx, `UPDATE simulation_runs SET status=$3,revision=revision+1,updated_at=NOW() WHERE id=$1::uuid AND status=$4 AND revision=$2`, c.Param("runID"), b.Revision, to, from)
	if e != nil || result.RowsAffected() != 1 {
		driverError(c, 409, "simulation_changed", "Refresh active run")
		return
	}
	e = adminAudit(c, tx, "simulation.run."+to, "", gin.H{"simulationRunId": c.Param("runID")})
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO simulation_events(simulation_run_id,kind,data)VALUES($1::uuid,$2,'{"scope":"movement and queued actions only; backend clocks and socket offers continue"}'::jsonb)`, c.Param("runID"), "run."+to)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Pause unavailable")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"status": to, "revision": b.Revision + 1}})
}
