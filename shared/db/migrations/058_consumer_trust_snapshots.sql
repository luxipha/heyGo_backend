-- Preserve only the consumer snapshot already disclosed by /auth/me.
-- This is snapshot history, not the provider's calculation/event ledger.
CREATE TABLE consumer_trust_snapshots (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 score INTEGER NOT NULL,
 status TEXT NOT NULL,
 calculated_at TIMESTAMPTZ,
 policy_version TEXT NOT NULL,
 recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX consumer_trust_snapshots_owner_idx ON consumer_trust_snapshots(user_id,id DESC);
CREATE FUNCTION record_consumer_trust_snapshot() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.consumer_trust_score IS NULL THEN
   DELETE FROM consumer_trust_snapshots WHERE user_id=NEW.id;
   RETURN NEW;
 END IF;
 IF TG_OP='UPDATE' AND ROW(NEW.consumer_trust_score,NEW.consumer_trust_status,NEW.trust_score_updated_at,NEW.trust_score_policy_version)
   IS NOT DISTINCT FROM ROW(OLD.consumer_trust_score,OLD.consumer_trust_status,OLD.trust_score_updated_at,OLD.trust_score_policy_version) THEN
   RETURN NEW;
 END IF;
 INSERT INTO consumer_trust_snapshots(user_id,score,status,calculated_at,policy_version)
 VALUES(NEW.id,NEW.consumer_trust_score,COALESCE(NEW.consumer_trust_status,''),NEW.trust_score_updated_at,COALESCE(NEW.trust_score_policy_version,''));
 RETURN NEW;
END $$;
CREATE TRIGGER consumer_trust_snapshot_record AFTER INSERT OR UPDATE OF consumer_trust_score,consumer_trust_status,trust_score_updated_at,trust_score_policy_version ON users
 FOR EACH ROW EXECUTE FUNCTION record_consumer_trust_snapshot();
INSERT INTO consumer_trust_snapshots(user_id,score,status,calculated_at,policy_version)
 SELECT id,consumer_trust_score,COALESCE(consumer_trust_status,''),trust_score_updated_at,COALESCE(trust_score_policy_version,'') FROM users WHERE consumer_trust_score IS NOT NULL;
