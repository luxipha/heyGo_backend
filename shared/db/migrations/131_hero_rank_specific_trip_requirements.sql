-- Keep immutable policy history. Existing versions inherit their former shared
-- minimum; new versions can specify an independent trip requirement per rank.
ALTER TABLE heroes_policies ADD COLUMN rank_trip_requirements JSONB;
ALTER TABLE heroes_policies ADD CONSTRAINT hero_rank_trip_requirements_object CHECK(rank_trip_requirements IS NULL OR jsonb_typeof(rank_trip_requirements)='object');
CREATE VIEW hero_rank_driver_activity AS
SELECT r.hero_id,r.driver_id,r.onboarded_at IS NOT NULL AS onboarded,
 CASE WHEN a.id IS NOT NULL THEN
 (SELECT count(*) FROM trips t WHERE t.status='completed' AND t.created_at>=a.linked_at
 AND (a.deadline_at IS NULL OR t.completed_at<=a.deadline_at)
 AND t.rider_id IS DISTINCT FROM t.assigned_driver_id AND t.assigned_driver_id=a.invitee_id
 AND NOT EXISTS(SELECT 1 FROM simulation_participants sp WHERE sp.actor_user_id=t.rider_id OR sp.actor_user_id=t.assigned_driver_id))
 ELSE
 (SELECT count(*) FROM trips t WHERE t.status='completed' AND t.created_at>=r.claimed_at
 AND t.rider_id IS DISTINCT FROM t.assigned_driver_id AND t.assigned_driver_id=r.driver_id
 AND NOT EXISTS(SELECT 1 FROM simulation_participants sp WHERE sp.actor_user_id=t.rider_id OR sp.actor_user_id=t.assigned_driver_id))
 END AS completed_trips
FROM heroes_all_referrals r LEFT JOIN referral_attributions a ON a.id=r.id;
CREATE VIEW hero_rank_progress AS
SELECT h.id AS hero_id,requirement.rank,requirement.required_drivers,requirement.minimum_trips,
 count(activity.driver_id) FILTER(WHERE activity.onboarded AND activity.completed_trips>=requirement.minimum_trips)::int AS qualified_drivers
FROM heroes_accounts h
CROSS JOIN (SELECT ranks,rank_trip_requirements,rank_minimum_trips FROM heroes_policies ORDER BY version DESC LIMIT 1)policy
CROSS JOIN LATERAL jsonb_each_text(policy.ranks)threshold
CROSS JOIN LATERAL(SELECT threshold.key AS rank,threshold.value::int AS required_drivers,
 COALESCE((policy.rank_trip_requirements->>threshold.key)::int,CASE WHEN threshold.key='E' THEN 0 ELSE policy.rank_minimum_trips END) AS minimum_trips)requirement
LEFT JOIN hero_rank_driver_activity activity ON activity.hero_id=h.id
GROUP BY h.id,requirement.rank,requirement.required_drivers,requirement.minimum_trips;
