-- Rank policy is versioned independently from captured referral payout rules.
ALTER TABLE heroes_policies ADD COLUMN rank_minimum_trips INT NOT NULL DEFAULT 0 CHECK(rank_minimum_trips BETWEEN 0 AND 10000);
CREATE VIEW hero_rank_drivers AS
SELECT r.hero_id,r.driver_id,r.onboarded_at IS NOT NULL AND
 CASE WHEN a.id IS NOT NULL THEN
 (SELECT count(*) FROM trips t WHERE t.status='completed' AND t.created_at>=a.linked_at
 AND (a.deadline_at IS NULL OR t.completed_at<=a.deadline_at)
 AND t.rider_id IS DISTINCT FROM t.assigned_driver_id AND t.assigned_driver_id=a.invitee_id
 AND NOT EXISTS(SELECT 1 FROM simulation_participants sp WHERE sp.actor_user_id=t.rider_id OR sp.actor_user_id=t.assigned_driver_id))
 ELSE
 (SELECT count(*) FROM trips t WHERE t.status='completed' AND t.created_at>=r.claimed_at
 AND t.rider_id IS DISTINCT FROM t.assigned_driver_id AND t.assigned_driver_id=r.driver_id
 AND NOT EXISTS(SELECT 1 FROM simulation_participants sp WHERE sp.actor_user_id=t.rider_id OR sp.actor_user_id=t.assigned_driver_id))
 END >= p.rank_minimum_trips AS qualifies
FROM heroes_all_referrals r LEFT JOIN referral_attributions a ON a.id=r.id
CROSS JOIN (SELECT rank_minimum_trips FROM heroes_policies ORDER BY version DESC LIMIT 1)p;
