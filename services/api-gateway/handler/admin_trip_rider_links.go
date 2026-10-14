package handler

import (
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// These projections deliberately contain IDs and decision state, never the
// fare's route JSON, coordinates, contact data, message text, or rating comment.
func (a *adminAPI) staffTripLinks(ctx *gin.Context, id uuid.UUID) (gin.H, error) {
	links := gin.H{}
	queries := map[string]string{
		"lifecycle": `SELECT jsonb_build_object(
			'createdAt',t.created_at,'arrivedAt',t.arrived_at,'startedAt',t.started_at,
			'completedAt',t.completed_at,'cancelledAt',t.cancelled_at,
			'assignments',COALESCE((SELECT jsonb_agg(jsonb_build_object('driverId',a.driver_id,'attempt',a.attempt,'status',a.status,'offeredAt',a.offered_at,'respondedAt',a.responded_at) ORDER BY a.attempt) FROM driver_assignments a WHERE a.trip_id=t.id),'[]'::jsonb)) FROM trips t WHERE t.id=$1`,
		"fareSnapshot":    `SELECT jsonb_build_object('id',f.id,'packageSlug',f.package_slug,'fareKobo',ROUND(f.total_fare_minor)::BIGINT,'noShowDebtKobo',f.no_show_debt_kobo,'createdAt',f.created_at,'marketCode',t.market_code,'pricingMarketCode',f.pricing_market_code,'pricingVersionId',f.pricing_version_id,'pricingDestination',CASE WHEN f.pricing_version_id IS NULL THEN NULL ELSE '/admin/pricing/'||f.pricing_version_id::TEXT END,'originRegionCode',t.origin_region_code,'destinationRegionCode',t.destination_region_code,'regulatoryTags',t.regulatory_tags,'classificationGeofences',t.classification_geofences,'classifiedAt',t.classified_at,'routeEstimate',jsonb_build_object('distanceMeters',CASE WHEN jsonb_typeof(f.route #> '{routes,0,distance}')='number' THEN (f.route #>> '{routes,0,distance}')::NUMERIC ELSE NULL END,'durationSeconds',CASE WHEN jsonb_typeof(f.route #> '{routes,0,duration}')='number' THEN (f.route #>> '{routes,0,duration}')::NUMERIC ELSE NULL END)) FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id WHERE t.id=$1`,
		"vehicle":         `SELECT COALESCE((SELECT jsonb_build_object('driverId',v.driver_id,'packageSlug',v.package_slug,'make',v.make,'model',v.model,'modelYear',v.model_year,'color',v.color,'reviewStatus',v.review_status,'source','current_driver_profile') FROM trips t JOIN driver_vehicles v ON v.driver_id=t.assigned_driver_id WHERE t.id=$1),'null'::jsonb)`,
		"settlement":      `SELECT COALESCE((SELECT jsonb_build_object('tripId',s.trip_id,'status',s.status,'expectedAmountKobo',s.expected_amount_kobo,'fareAmountKobo',s.fare_amount_kobo,'noShowDebtKobo',s.no_show_debt_kobo,'updatedAt',s.updated_at,'resolvedAt',s.resolved_at,'evidence','driver_assertion') FROM trip_settlements s WHERE s.trip_id=$1),'null'::jsonb)`,
		"ratings":         `SELECT COALESCE(jsonb_agg(jsonb_build_object('actorId',r.actor_id,'subjectId',r.subject_id,'rating',r.rating,'reviewStatus',r.admin_review_status,'createdAt',r.created_at) ORDER BY r.created_at) FILTER (WHERE r.trip_id IS NOT NULL),'[]'::jsonb) FROM trip_ratings r WHERE r.trip_id=$1`,
		"statusEvents":    `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',e.id,'previousStatus',e.previous_status,'status',e.status,'recordedAt',e.recorded_at,'source',e.source) ORDER BY e.id) FILTER (WHERE e.id IS NOT NULL),'[]'::jsonb) FROM trip_status_events e WHERE e.trip_id=$1`,
		"historyCoverage": `SELECT jsonb_build_object('statusEvents',CASE WHEN EXISTS(SELECT 1 FROM trip_status_events e WHERE e.trip_id=t.id AND e.previous_status IS NULL) THEN 'complete_since_create' ELSE 'partial_or_unavailable' END,'vehicleSnapshot',CASE WHEN t.accepted_vehicle_snapshot IS NOT NULL THEN 'captured_at_acceptance' ELSE 'unavailable' END) FROM trips t WHERE t.id=$1`,
	}
	for key, query := range queries {
		value, err := a.staffJSON(ctx, query, id)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		links[key] = value
	}
	vehicleSnapshotQuery := `SELECT CASE WHEN t.accepted_vehicle_snapshot IS NULL THEN 'null'::jsonb ELSE jsonb_build_object('vehicle',t.accepted_vehicle_snapshot - 'plate','capturedAt',t.accepted_vehicle_snapshot_at) END FROM trips t WHERE t.id=$1`
	if adminHasPermission(ctx, permissionDriverRead) || adminHasPermission(ctx, permissionSafetyRead) || adminHasPermission(ctx, permissionSupportRead) {
		vehicleSnapshotQuery = `SELECT CASE WHEN t.accepted_vehicle_snapshot IS NULL THEN 'null'::jsonb ELSE jsonb_build_object('vehicle',t.accepted_vehicle_snapshot,'capturedAt',t.accepted_vehicle_snapshot_at) END FROM trips t WHERE t.id=$1`
	}
	vehicleSnapshot, err := a.staffJSON(ctx, vehicleSnapshotQuery, id)
	if err != nil {
		return nil, err
	}
	links["vehicleSnapshot"] = vehicleSnapshot
	if adminHasPermission(ctx, permissionFinanceRead) {
		value, err := a.staffJSON(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',c.id,'status',c.status,'feeKobo',c.fee_kobo,'policyId',c.policy_id,'policyVersion',p.version,'policyWaitMinutes',p.wait_minutes,'marketCode',c.market_code,'submittedAt',c.submitted_at,'reviewedAt',c.reviewed_at,'destination','/admin/no-show/claims/'||c.id::text)) FILTER (WHERE c.id IS NOT NULL),'[]'::jsonb) FROM driver_no_show_claims c JOIN no_show_policies p ON p.id=c.policy_id WHERE c.trip_id=$1`, id)
		if err != nil {
			return nil, err
		}
		links["claims"] = value
		value, err = a.staffJSON(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',c.id,'ruleId',c.rule_id,'amountKobo',c.amount_kobo,'bearer',c.bearer,'fundingSource',c.funding_source,'ruleSnapshot',c.rule_snapshot,'assessedAt',c.assessed_at) ORDER BY c.assessed_at) FILTER (WHERE c.id IS NOT NULL),'[]'::jsonb) FROM trip_statutory_charges c WHERE c.trip_id=$1`, id)
		if err != nil {
			return nil, err
		}
		links["policyCharges"] = value
		value, err = a.staffJSON(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('debtId',s.debt_id,'allocationStatus',s.status,'amountKobo',s.amount_kobo,'paymentId',p.id,'paymentStatus',p.status,'paymentProvider',p.provider,'paymentCreatedAt',p.created_at) ORDER BY s.debt_id) FILTER (WHERE s.debt_id IS NOT NULL),'[]'::jsonb) FROM trip_no_show_debt_settlements s LEFT JOIN LATERAL (SELECT id,status,provider,created_at FROM rider_no_show_debt_payments WHERE debt_id=s.debt_id ORDER BY created_at DESC,id DESC LIMIT 1) p ON TRUE WHERE s.trip_id=$1`, id)
		if err != nil {
			return nil, err
		}
		links["debtPaymentStates"] = value
	}
	if adminHasPermission(ctx, permissionSupportRead) {
		value, err := a.staffJSON(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',c.id,'status',c.status,'priority',c.priority,'topicCode',c.topic_code,'destination','/admin/support/cases/'||c.id::text) ORDER BY c.created_at DESC) FILTER (WHERE c.id IS NOT NULL),'[]'::jsonb) FROM support_cases c WHERE c.trip_id=$1`, id)
		if err != nil {
			return nil, err
		}
		links["supportCases"] = value
	}
	return links, nil
}

func (a *adminAPI) staffRiderLinks(ctx *gin.Context, id uuid.UUID) (gin.H, error) {
	links := gin.H{}
	// Account status is in the parent Rider object. These links expose only
	// records the caller's module permission permits them to open.
	if adminHasPermission(ctx, permissionFinanceRead) {
		value, err := a.staffJSON(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',d.id,'claimId',d.claim_id,'status',d.status,'amountKobo',d.amount_kobo,'createdAt',d.created_at,'settledAt',d.settled_at) ORDER BY d.created_at DESC) FILTER (WHERE d.id IS NOT NULL),'[]'::jsonb) FROM rider_no_show_debts d WHERE d.rider_id=$1`, id)
		if err != nil {
			return nil, err
		}
		links["debts"] = value
	}
	if adminHasPermission(ctx, permissionSupportRead) {
		value, err := a.staffJSON(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',c.id,'tripId',c.trip_id,'status',c.status,'priority',c.priority,'topicCode',c.topic_code,'destination','/admin/support/cases/'||c.id::text) ORDER BY c.created_at DESC) FILTER (WHERE c.id IS NOT NULL),'[]'::jsonb) FROM support_cases c WHERE c.opened_by=$1 AND c.audience='rider'`, id)
		if err != nil {
			return nil, err
		}
		links["supportCases"] = value
	}
	if adminHasPermission(ctx, permissionSafetyRead) {
		value, err := a.staffJSON(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',i.id,'caseId',i.case_id,'status',i.status,'severity',i.severity,'destination','/admin/safety/incidents/'||i.id::text) ORDER BY i.created_at DESC) FILTER (WHERE i.id IS NOT NULL),'[]'::jsonb) FROM safety_incidents i JOIN support_cases c ON c.id=i.case_id WHERE c.opened_by=$1 AND c.audience='rider'`, id)
		if err != nil {
			return nil, err
		}
		links["safetyIncidents"] = value
	}
	if adminHasPermission(ctx, permissionRiderFlags) {
		canSupport := adminHasPermission(ctx, permissionSupportWrite)
		canSafety := adminHasPermission(ctx, permissionSafetyWrite)
		var raw []byte
		err := a.pool.QueryRow(ctx.Request.Context(), `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',f.id,'kind',f.kind,'caseId',f.case_id,'incidentId',f.incident_id,'status',f.status,'reviewAt',f.review_at,'destination','/admin/riders/'||f.rider_id::text||'/flags') ORDER BY f.created_at DESC) FILTER (WHERE f.id IS NOT NULL),'[]'::jsonb) FROM rider_flags f WHERE f.rider_id=$1 AND (($2 AND f.kind IN ('support','payment')) OR ($3 AND f.kind='safety'))`, id, canSupport, canSafety).Scan(&raw)
		if err != nil {
			return nil, err
		}
		links["flags"] = json.RawMessage(raw)
	}
	return links, nil
}

func (a *adminAPI) staffJSON(ctx *gin.Context, query string, id uuid.UUID) (json.RawMessage, error) {
	var value []byte
	if err := a.pool.QueryRow(ctx.Request.Context(), query, id).Scan(&value); err != nil {
		return nil, err
	}
	return json.RawMessage(value), nil
}
