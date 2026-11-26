-- Preserve the original first-stage snapshot. A separate activation stage reserves
-- NGN 2,000 from it; all credits and payments use hero_stage_amount().
CREATE TABLE driver_first_online (
 driver_id UUID PRIMARY KEY REFERENCES users(id),
 first_online_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TRIGGER driver_first_online_immutable BEFORE UPDATE OR DELETE ON driver_first_online
 FOR EACH ROW EXECUTE FUNCTION heroes_immutable_record();

ALTER TABLE hero_referral_stages DROP CONSTRAINT hero_referral_stages_position_check;
ALTER TABLE hero_referral_stages DROP CONSTRAINT hero_referral_stages_trip_target_check;
ALTER TABLE hero_referral_stages ADD COLUMN enabled BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE hero_referral_stages ADD CONSTRAINT hero_stage_target CHECK
 ((position=0 AND trip_target=0 AND reward_kobo=200000) OR (position>0 AND trip_target BETWEEN 1 AND 10000));

CREATE FUNCTION hero_stage_amount(stage UUID) RETURNS BIGINT LANGUAGE SQL STABLE AS $$
 SELECT CASE WHEN NOT s.enabled THEN 0 ELSE s.reward_kobo - CASE WHEN s.position=1
 THEN COALESCE((SELECT a.reward_kobo FROM hero_referral_stages a WHERE a.referral_id=s.referral_id AND a.position=0 AND a.enabled),0) ELSE 0 END END
 FROM hero_referral_stages s WHERE s.id=stage
$$;

CREATE OR REPLACE FUNCTION hero_stage_protect() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Stage history is permanent'; END IF;
 IF (to_jsonb(NEW)-ARRAY['qualified_at','credited_at','approved_by','enabled']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['qualified_at','credited_at','approved_by','enabled']) OR
 (OLD.qualified_at IS NOT NULL AND NEW.qualified_at IS DISTINCT FROM OLD.qualified_at) OR
 (OLD.credited_at IS NOT NULL AND NEW IS DISTINCT FROM OLD) THEN RAISE EXCEPTION 'Stage rules and credits are immutable'; END IF;
 IF NEW.enabled IS DISTINCT FROM OLD.enabled AND (OLD.position<>0 OR OLD.qualified_at IS NOT NULL OR NEW.qualified_at IS NOT NULL OR EXISTS
 (SELECT 1 FROM hero_referral_stages p WHERE p.referral_id=OLD.referral_id AND p.position=1 AND p.qualified_at IS NOT NULL)) THEN
 RAISE EXCEPTION 'Earned activation allocations are permanent'; END IF;
 IF NOT NEW.enabled AND NEW.qualified_at IS NOT NULL THEN RAISE EXCEPTION 'Disabled stages cannot qualify'; END IF;
 RETURN NEW;
END $$;

CREATE FUNCTION sync_hero_activation(early BOOLEAN, referral UUID DEFAULT NULL) RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE p hero_referral_stages; a hero_referral_stages;
BEGIN
 FOR p IN SELECT s.* FROM hero_referral_stages s JOIN referral_attributions r ON r.id=s.referral_id
 WHERE s.position=1 AND s.reward_kobo>200000 AND s.qualified_at IS NULL
 AND r.inviter_role='hero' AND r.invitee_role='driver' AND (referral IS NULL OR s.referral_id=referral)
 ORDER BY s.id FOR UPDATE OF s LOOP
 SELECT * INTO a FROM hero_referral_stages WHERE referral_id=p.referral_id AND position=0 FOR UPDATE;
 IF FOUND THEN
 -- Preserve an earned activation even if maintenance has not processed it yet.
 IF a.enabled AND a.qualified_at IS NULL AND EXISTS(
 SELECT 1 FROM referral_attributions r JOIN driver_first_online o ON o.driver_id=r.invitee_id
 WHERE r.id=p.referral_id AND o.first_online_at>=r.linked_at AND (r.deadline_at IS NULL OR o.first_online_at<=r.deadline_at)) THEN
 UPDATE hero_referral_stages SET qualified_at=now() WHERE id=a.id;
 ELSIF a.qualified_at IS NULL AND a.enabled IS DISTINCT FROM early THEN
 UPDATE hero_referral_stages SET enabled=early WHERE id=a.id;
 END IF;
 ELSIF early THEN
 INSERT INTO hero_referral_stages(id,referral_id,position,trip_target,reward_kobo,approval_mode,payout_mode)
 VALUES(gen_random_uuid(),p.referral_id,0,0,200000,'manual',p.payout_mode);
 END IF;
 END LOOP;
END $$;

CREATE OR REPLACE VIEW heroes_all_referrals AS
 SELECT id,hero_id,driver_id,policy_id,claimed_at,onboarded_at,qualified_at FROM heroes_referrals
 UNION ALL
 SELECT a.id,a.hero_id,a.invitee_id,a.id,a.linked_at,a.onboarded_at,
 CASE WHEN EXISTS(SELECT 1 FROM hero_referral_stages s WHERE s.referral_id=a.id)
 THEN (SELECT max(s.credited_at) FROM hero_referral_stages s WHERE s.referral_id=a.id AND s.enabled HAVING bool_and(s.credited_at IS NOT NULL))
 ELSE a.credited_at END
 FROM referral_attributions a WHERE a.inviter_role='hero';
