package handler

import (
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/simulation"
	"io"
	"net/http"
	"strings"
)

func registerSimulationRoutes(g *gin.RouterGroup, a *adminAPI) {
	g.POST("/simulation/runs/:runID/participants/:participantID/rider-actions", a.requireCSRF, a.queueSimulationRider)
	g.POST("/simulation/runs/:runID/population", a.requireCSRF, a.batchSimulationParticipants)
	g.POST("/simulation/runs/:runID/pause", a.requireCSRF, a.pauseSimulation)
	g.POST("/simulation/runs/:runID/participants/:participantID/actions", a.requireCSRF, a.queueSimulationRider)
	g.POST("/simulation/runs/:runID/recover", a.requireCSRF, a.recoverSimulation)
	g.POST("/simulation/runs/:runID/clone", a.requireCSRF, a.cloneSimulation)
	g.POST("/simulation/runs/:runID/start", a.requireCSRF, a.startSimulation)
	g.POST("/simulation/runs/:runID/stop", a.requireCSRF, a.stopSimulation)
	g.POST("/simulation/runs/:runID/participants/:participantID/offer-response", a.requireCSRF, a.respondSimulationOffer)
	g.GET("/simulation/runs/:runID/matching/:tripID", a.simulationMatchingTrace)
	g.GET("/simulation/runs", a.simulationRuns)
	g.POST("/simulation/runs", a.requireCSRF, a.createSimulationRun)
	g.GET("/simulation/runs/:runID", a.simulationRun)
	g.POST("/simulation/runs/:runID/participants", a.requireCSRF, a.addSimulationParticipant)
}
func simulationBody(c *gin.Context, v any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16384)
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return false
	}
	var trailing any
	return d.Decode(&trailing) == io.EOF
}
func simulationID(c *gin.Context) bool {
	_, e := uuid.Parse(c.Param("runID"))
	if e != nil {
		driverError(c, 400, "invalid_run", "Valid run ID required")
		return false
	}
	return true
}
func (a *adminAPI) simulationRuns(c *gin.Context) {
	var raw []byte
	e := a.pool.QueryRow(c.Request.Context(), `SELECT COALESCE(jsonb_agg(x ORDER BY x.created_at DESC,x.id DESC),'[]'::jsonb) FROM (SELECT id,name,is_simulated AS "isSimulated",seed,status,revision,created_at FROM simulation_runs ORDER BY created_at DESC,id DESC LIMIT 50)x`).Scan(&raw)
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Simulation runs unavailable")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"items": json.RawMessage(raw), "limit": 50}})
}
func (a *adminAPI) createSimulationRun(c *gin.Context) {
	var b struct {
		Name string `json:"name"`
		Seed int64  `json:"seed"`
	}
	if !simulationBody(c, &b) || strings.TrimSpace(b.Name) == "" || len(b.Name) > 120 || b.Seed < 0 {
		driverError(c, 400, "invalid_run", "Name and nonnegative seed required")
		return
	}
	id := uuid.NewString()
	tx, e := a.pool.Begin(c.Request.Context())
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Cannot create run")
		return
	}
	defer tx.Rollback(c.Request.Context())
	_, e = tx.Exec(c.Request.Context(), `INSERT INTO simulation_runs(id,name,seed,created_by)VALUES($1::uuid,$2,$3,$4::uuid)`, id, strings.TrimSpace(b.Name), b.Seed, currentAdmin(c).ID)
	if e == nil {
		_, e = tx.Exec(c.Request.Context(), `INSERT INTO simulation_events(simulation_run_id,kind,data)VALUES($1::uuid,'run.created',jsonb_build_object('seed',$2::bigint))`, id, b.Seed)
	}
	if e == nil {
		e = adminAudit(c, tx, "simulation.run.created", "", gin.H{"simulationRunId": id, "seed": b.Seed})
	}
	if e == nil {
		e = tx.Commit(c.Request.Context())
	}
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Cannot create run")
		return
	}
	c.JSON(201, contracts.APIResponse{Data: gin.H{"id": id, "name": strings.TrimSpace(b.Name), "seed": b.Seed, "isSimulated": true, "status": "draft", "revision": 1}})
}
func (a *adminAPI) simulationRun(c *gin.Context) {
	if !simulationID(c) {
		return
	}
	var raw []byte
	e := a.pool.QueryRow(c.Request.Context(), `SELECT jsonb_build_object('id',r.id,'name',r.name,'seed',r.seed,'requestConcurrency',r.request_concurrency,'isSimulated',r.is_simulated,'status',r.status, 'revision',r.revision,'serverTime',NOW(),'summary',jsonb_build_object(
 'participants',(SELECT count(*) FROM simulation_participants p WHERE p.simulation_run_id=r.id),
 'trips',(SELECT count(*) FROM trips t WHERE EXISTS(SELECT 1 FROM simulation_participants p WHERE p.simulation_run_id=r.id AND(p.actor_user_id=t.rider_id OR p.actor_user_id=t.assigned_driver_id OR EXISTS(SELECT 1 FROM driver_assignments a WHERE a.trip_id=t.id AND a.driver_id=p.actor_user_id)))),
 'tripStatuses',COALESCE((SELECT jsonb_object_agg(status,n)FROM(SELECT t.status,count(*) AS n FROM trips t WHERE EXISTS(SELECT 1 FROM simulation_participants p WHERE p.simulation_run_id=r.id AND(p.actor_user_id=t.rider_id OR p.actor_user_id=t.assigned_driver_id OR EXISTS(SELECT 1 FROM driver_assignments a WHERE a.trip_id=t.id AND a.driver_id=p.actor_user_id)))GROUP BY t.status)x),'{}'::jsonb),
 'queuedActions',(SELECT count(*) FROM simulation_rider_actions a WHERE a.simulation_run_id=r.id AND a.status IN ('queued','processing')),
 'failedActions',(SELECT count(*) FROM simulation_rider_actions a WHERE a.simulation_run_id=r.id AND a.status='failed'),
 'averageActionMs',(SELECT round(avg(extract(epoch FROM(a.processed_at-a.created_at))*1000)) FROM simulation_rider_actions a WHERE a.simulation_run_id=r.id AND a.processed_at IS NOT NULL),
 'oldestQueuedSeconds',(SELECT round(max(extract(epoch FROM(NOW()-a.created_at)))) FROM simulation_rider_actions a WHERE a.simulation_run_id=r.id AND a.status IN ('queued','processing')),
 'offers',(SELECT count(*)FROM driver_assignments a WHERE EXISTS(SELECT 1 FROM simulation_participants p WHERE p.simulation_run_id=r.id AND p.actor_user_id=a.driver_id)),
 'averageOfferResponseMs',(SELECT round(avg(extract(epoch FROM(a.responded_at-a.offered_at))*1000))FROM driver_assignments a WHERE a.responded_at IS NOT NULL AND EXISTS(SELECT 1 FROM simulation_participants p WHERE p.simulation_run_id=r.id AND p.actor_user_id=a.driver_id)),
 'doubleAssignedDrivers',(SELECT count(*)FROM(SELECT a.driver_id FROM driver_assignments a WHERE a.status IN ('offered','accepted') AND EXISTS(SELECT 1 FROM simulation_participants p WHERE p.simulation_run_id=r.id AND p.actor_user_id=a.driver_id)GROUP BY a.driver_id HAVING count(*)>1)x)
 ),'trips' ,COALESCE((SELECT jsonb_agg(x ORDER BY x."updatedAt" DESC)FROM(SELECT t.id,t.rider_id AS "riderId",t.status,t.assigned_driver_id AS "driverId",ROUND(f.total_fare_minor)::bigint AS "fareKobo",t.no_show_debt_kobo AS "noShowDebtKobo",GREATEST(0,EXTRACT(EPOCH FROM(COALESCE(t.started_at,t.cancelled_at,t.completed_at,NOW())-t.arrived_at)))::int AS "waitingSeconds",
 (SELECT jsonb_build_object('id',pv.id,'version',pv.version,'marketCode',pv.market_code,'rates',(SELECT to_jsonb(pr) FROM pricing_package_rates pr WHERE pr.pricing_id=pv.id AND pr.package_slug=f.package_slug)) FROM pricing_versions pv WHERE pv.id=f.pricing_version_id) AS pricing,
 (SELECT jsonb_build_object('status',c.status,'feeKobo',c.fee_kobo,'eligibleAt',c.eligible_at,'debt',(SELECT jsonb_build_object('amountKobo',d.amount_kobo,'status',d.status,'settledAt',d.settled_at)FROM rider_no_show_debts d WHERE d.claim_id=c.id))FROM driver_no_show_claims c WHERE c.trip_id=t.id) AS "noShowClaim",
 (SELECT jsonb_build_object('status',st.status,'expectedAmountKobo',st.expected_amount_kobo,'resolvedAt',st.resolved_at)FROM trip_settlements st WHERE st.trip_id=t.id)AS settlement,
 COALESCE((SELECT jsonb_agg(jsonb_build_object('debtId',ds.debt_id,'amountKobo',ds.amount_kobo,'status',ds.status,'beneficiaryDriverId',ds.beneficiary_driver_id,'settledAt',ds.settled_at))FROM trip_no_show_debt_settlements ds WHERE ds.trip_id=t.id),'[]'::jsonb)AS "debtRecovery",f.package_slug AS "categorySlug",t.arrived_at AS "arrivedAt",t.started_at AS "startedAt",t.completed_at AS "completedAt",t.updated_at AS "updatedAt",jsonb_build_object('latitude',ST_Y(f.pickup::geometry),'longitude',ST_X(f.pickup::geometry))AS pickup,jsonb_build_object('latitude',ST_Y(f.destination::geometry),'longitude',ST_X(f.destination::geometry))AS destination FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id WHERE EXISTS(SELECT 1 FROM simulation_participants p WHERE p.simulation_run_id=r.id AND(p.actor_user_id=t.rider_id OR p.actor_user_id=t.assigned_driver_id OR EXISTS(SELECT 1 FROM driver_assignments a WHERE a.trip_id=t.id AND a.driver_id=p.actor_user_id)))ORDER BY t.updated_at DESC LIMIT 50)x),'[]'::jsonb),'participants',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',p.id,'role',p.role,'name',p.name,'controller',p.controller,'isSimulated',p.is_simulated,'runtimeStatus',p.runtime_status,'actorUserId',p.actor_user_id,'configuration',p.configuration,'live',(SELECT jsonb_build_object('status',COALESCE(d.status,'not_registered'),'available',COALESCE(d.available,FALSE),'onlineRequested',COALESCE(d.online_requested,FALSE),'latitude',ST_Y(l.location::geometry),'longitude',ST_X(l.location::geometry),'recordedAt',l.recorded_at,'eligibleToDrive',e.eligible_to_drive,'autoAccept',(SELECT auto_accept FROM driver_ride_preferences WHERE driver_id=u.id),'categorySlug',(SELECT package_slug FROM driver_vehicles WHERE driver_id=u.id),'balanceKobo',(SELECT balance_kobo FROM driver_operating_accounts b WHERE b.driver_id=u.id))FROM users u LEFT JOIN drivers d ON d.id=u.id LEFT JOIN driver_live_locations l ON l.driver_id=u.id LEFT JOIN driver_eligibility e ON e.driver_id=u.id WHERE u.id=p.actor_user_id AND 'driver'=ANY(u.roles)),'offer',(SELECT jsonb_build_object('tripId',a.trip_id,'expiresAt',a.expires_at)FROM driver_assignments a WHERE a.driver_id=p.actor_user_id AND a.status='offered'AND a.expires_at>NOW()ORDER BY a.offered_at DESC LIMIT 1))ORDER BY p.created_at,p.id) FROM simulation_participants p WHERE p.simulation_run_id=r.id),'[]'::jsonb),'riderActions',COALESCE((SELECT jsonb_agg(x ORDER BY x.created_at)FROM(SELECT id,participant_id AS "participantId",action,status,result,error,created_at FROM simulation_rider_actions WHERE simulation_run_id=r.id ORDER BY created_at DESC LIMIT 200)x),'[]'::jsonb),'movementRoutes',COALESCE((SELECT jsonb_agg(x)FROM(SELECT DISTINCT ON(e.participant_id)e.participant_id AS "participantId",e.data->>'tripId' AS "tripId",e.data->>'leg' AS leg,e.data->'coordinates' AS coordinates FROM simulation_events e JOIN simulation_participants p ON p.id=e.participant_id JOIN trips t ON t.id::text=e.data->>'tripId' AND t.assigned_driver_id=p.actor_user_id WHERE e.simulation_run_id=r.id AND e.kind='movement.route_ready' AND ((t.status IN ('accepted','arrived') AND e.data->>'leg'='pickup')OR(t.status='started' AND e.data->>'leg'='destination'))ORDER BY e.participant_id,e.id DESC)x),'[]'::jsonb),'matchingSnapshots',COALESCE((SELECT jsonb_agg(x ORDER BY x.id)FROM(SELECT id,kind,data-'candidates' AS data,recorded_at FROM simulation_events WHERE simulation_run_id=r.id AND kind='matching.candidates' ORDER BY id DESC LIMIT 50)x),'[]'::jsonb),'events',COALESCE((SELECT jsonb_agg(x ORDER BY x.id)FROM(SELECT id,participant_id AS "participantId",kind,CASE WHEN kind='matching.candidates' THEN data-'candidates' ELSE data END AS data,recorded_at FROM simulation_events WHERE simulation_run_id=r.id ORDER BY id DESC LIMIT 200)x),'[]'::jsonb))FROM simulation_runs r WHERE r.id=$1::uuid`, c.Param("runID")).Scan(&raw)
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			driverError(c, 404, "run_not_found", "Simulation run not found")
		} else {
			driverError(c, 503, "simulation_unavailable", "Simulation storage unavailable")
		}
		return
	}
	c.JSON(200, contracts.APIResponse{Data: json.RawMessage(raw)})
}
func (a *adminAPI) addSimulationParticipant(c *gin.Context) {
	if !simulationID(c) {
		return
	}
	var b struct {
		Revision    int64                  `json:"revision"`
		Participant simulation.Participant `json:"participant"`
	}
	if !simulationBody(c, &b) {
		driverError(c, 400, "invalid_participant", "Valid participant configuration required")
		return
	}
	p := b.Participant
	p.Name = strings.TrimSpace(p.Name)
	if e := p.Validate(); e != nil {
		driverError(c, 400, "invalid_participant", e.Error())
		return
	}
	ctx := c.Request.Context()
	tx, e := a.pool.Begin(ctx)
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Cannot configure participant")
		return
	}
	defer tx.Rollback(ctx)
	var roleCount int
	e = tx.QueryRow(ctx, `SELECT count(*)FROM simulation_participants WHERE simulation_run_id=$1::uuid AND role=$2`, c.Param("runID"), p.Role).Scan(&roleCount)
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Cannot read population")
		return
	}
	limit := 1000
	if p.Role == "rider" {
		limit = 100
	}
	// Revision update serializes concurrent additions; a losing transaction rolls back.
	result, e := tx.Exec(ctx, `UPDATE simulation_runs SET revision=revision+1,updated_at=NOW() WHERE id=$1::uuid AND is_simulated AND status='draft' AND revision=$2`, c.Param("runID"), b.Revision)
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Cannot update run")
		return
	}
	if result.RowsAffected() != 1 {
		driverError(c, 409, "simulation_changed", "Reload the draft run before editing")
		return
	}
	if roleCount >= limit {
		driverError(c, 409, "population_limit", "Run population limit reached")
		return
	}
	if p.ModelID != "" {
		var valid bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM vehicle_catalogue WHERE id=$1::uuid AND $2 BETWEEN year_from AND year_to)`, p.ModelID, p.ModelYear).Scan(&valid)
		if e != nil || !valid {
			driverError(c, 400, "invalid_model", "Choose a configured model and valid year")
			return
		}
	}
	for _, id := range p.CategoryIDs {
		var valid bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ride_categories WHERE id=$1::uuid)`, id).Scan(&valid)
		if e != nil || !valid {
			driverError(c, 400, "invalid_category", "Choose an existing category")
			return
		}
	}
	id := uuid.NewString()
	raw, _ := json.Marshal(p)
	_, e = tx.Exec(ctx, `INSERT INTO simulation_participants(id,simulation_run_id,role,name,controller,configuration)VALUES($1::uuid,$2::uuid,$3,$4,$5,$6::jsonb)`, id, c.Param("runID"), p.Role, p.Name, p.Controller, raw)
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO simulation_events(simulation_run_id,participant_id,kind,data)VALUES($1::uuid,$2::uuid,'participant.configured',$3::jsonb)`, c.Param("runID"), id, raw)
	}
	if e == nil {
		e = adminAudit(c, tx, "simulation.participant.configured", "", gin.H{"simulationParticipantId": id, "simulationRunId": c.Param("runID"), "role": p.Role})
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Participant could not be saved")
		return
	}
	c.JSON(201, contracts.APIResponse{Data: gin.H{"id": id, "revision": b.Revision + 1, "runtimeStatus": "configured", "isSimulated": true}})
}

func (a *adminAPI) simulationMatchingTrace(c *gin.Context) {
	if !simulationID(c) {
		return
	}
	if _, e := uuid.Parse(c.Param("tripID")); e != nil {
		driverError(c, 400, "invalid_trip", "Valid trip required")
		return
	}
	var raw []byte
	e := a.pool.QueryRow(c.Request.Context(), `SELECT COALESCE(jsonb_agg(x ORDER BY x.id),'[]'::jsonb)FROM(SELECT id,kind,data,recorded_at FROM simulation_events WHERE simulation_run_id=$1::uuid AND kind='matching.candidates' AND data->>'tripId'=$2 ORDER BY id DESC LIMIT 10)x`, c.Param("runID"), c.Param("tripID")).Scan(&raw)
	if e != nil {
		driverError(c, 503, "simulation_unavailable", "Matching trace unavailable")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"items": json.RawMessage(raw), "limit": 10}})
}
