-- Keep existing command history; extend the closed action vocabulary for Driver clients.
ALTER TABLE simulation_rider_actions DROP CONSTRAINT simulation_rider_actions_action_check;
ALTER TABLE simulation_rider_actions ADD CONSTRAINT simulation_rider_actions_action_check CHECK(action IN ('search','preview','book','cancel','receipt','availability','autoAccept','noShowEligibility','noShowClaim','settlement','settlementConfirm'));
