package repo

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/luxipha/heyGo_backend/shared/driverstate"
)

// Observes the real predicate before dispatch; never supplies a dispatch decision.
// A savepoint keeps optional diagnostics failures from changing matching behaviour.
func simulationMatchingSnapshot(ctx context.Context, tx pgx.Tx, tripID, slug string, lat, lng, radius float64) {
	sp, e := tx.Begin(ctx)
	if e != nil {
		return
	}
	defer sp.Rollback(ctx)
	_, e = sp.Exec(ctx, `INSERT INTO simulation_events(simulation_run_id,kind,data)
 SELECT r.id,'matching.candidates',jsonb_build_object('tripId',$1::text,'radiusMeters',$5::double precision,'categorySlug',$2::text,'scope','nearest 200 run Drivers; unrelated marketplace supply may also compete','candidateLimit',200,'candidateCount',(SELECT count(*) FROM simulation_participants pc WHERE pc.simulation_run_id=r.id AND pc.role='driver'),'ranking','distance metres minus bounded trust advantage, then last_seen_at descending; locked rows can be skipped','candidates',COALESCE((SELECT jsonb_agg(jsonb_build_object(
 'participantId',p.id,'driverId',p.actor_user_id,'name',p.name,'status',d.status,
 'categoryEligible',COALESCE(driver_category_eligible(d.id,$2),FALSE),
 'verificationEligible',COALESCE(e.eligible_to_drive,FALSE),'available',COALESCE(d.available,FALSE),'onlineRequested',COALESCE(d.online_requested,FALSE),
 'socketFresh',EXISTS(SELECT 1 FROM driver_socket_sessions s WHERE s.driver_id=d.id AND s.expires_at>NOW()),
 'gpsFresh',COALESCE(l.recorded_at>=NOW()-INTERVAL '30 minutes',FALSE),
 'marketMatches',COALESCE(d.online_market_code=t.market_code AND driver_market_at(d.location)=t.market_code,FALSE),
 'balanceEligible',COALESCE(driver_balance_eligible(d.id,t.market_code),FALSE),
 'preferenceMatches',COALESCE(driver_ride_preference_matches(d.id,t.id),FALSE),
 'previouslyOffered',EXISTS(SELECT 1 FROM driver_assignments a WHERE a.trip_id=t.id AND a.driver_id=d.id),
 'distanceMeters',ST_Distance(d.location,ST_SetSRID(ST_MakePoint($4,$3),4326)::geography),
 'withinRadius',COALESCE(ST_DWithin(d.location,ST_SetSRID(ST_MakePoint($4,$3),4326)::geography,$5),FALSE),
 'rankScoreMeters',ST_Distance(d.location,ST_SetSRID(ST_MakePoint($4,$3),4326)::geography)-LEAST(GREATEST(COALESCE(u.provider_trust_score,100)-100,0)*2,500),
 'predicateEligible',COALESCE((`+driverstate.AvailabilityPredicate("t.market_code", "$2", "ST_SetSRID(ST_MakePoint($4,$3),4326)::geography", "$5")+`)AND driver_ride_preference_matches(d.id,t.id)AND NOT EXISTS(SELECT 1 FROM driver_assignments a WHERE a.trip_id=t.id AND a.driver_id=d.id),FALSE)
 )ORDER BY p.created_at)FROM (SELECT sp.* FROM simulation_participants sp LEFT JOIN drivers sd ON sd.id=sp.actor_user_id WHERE sp.simulation_run_id=r.id AND sp.role='driver' ORDER BY ST_Distance(sd.location,ST_SetSRID(ST_MakePoint($4,$3),4326)::geography) NULLS LAST,sp.id LIMIT 200)p LEFT JOIN drivers d ON d.id=p.actor_user_id LEFT JOIN users u ON u.id=d.id LEFT JOIN driver_eligibility e ON e.driver_id=d.id LEFT JOIN driver_live_locations l ON l.driver_id=d.id WHERE p.simulation_run_id=r.id AND p.role='driver'),'[]'::jsonb))
 FROM simulation_runs r JOIN trips t ON t.id=$1::uuid WHERE r.status='running' AND EXISTS(SELECT 1 FROM simulation_participants p WHERE p.simulation_run_id=r.id AND(p.actor_user_id=t.rider_id OR EXISTS(SELECT 1 FROM drivers d WHERE d.id=p.actor_user_id AND ST_DWithin(d.location,ST_SetSRID(ST_MakePoint($4,$3),4326)::geography,$5))))`, tripID, slug, lat, lng, radius)
	if e == nil {
		sp.Commit(ctx)
	}
}
