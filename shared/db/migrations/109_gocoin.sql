-- Coins are loyalty units; only an explicit redemption creates monetary credit.
CREATE TABLE gocoin_policies (
 version BIGSERIAL PRIMARY KEY, earning_enabled BOOLEAN NOT NULL DEFAULT false,
 redemption_enabled BOOLEAN NOT NULL DEFAULT false,
 coins_per_block BIGINT NOT NULL DEFAULT 0 CHECK(coins_per_block BETWEEN 0 AND 1000000),
 credit_kobo_per_block BIGINT NOT NULL DEFAULT 0 CHECK(credit_kobo_per_block BETWEEN 0 AND 100000000),
 categories JSONB NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(categories)='object'),
 created_by UUID REFERENCES admin_users(id), created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 CHECK(NOT redemption_enabled OR (coins_per_block>0 AND credit_kobo_per_block>0))
);
INSERT INTO gocoin_policies DEFAULT VALUES;
CREATE TRIGGER gocoin_policy_immutable BEFORE UPDATE OR DELETE ON gocoin_policies FOR EACH ROW EXECUTE FUNCTION heroes_immutable_record();
CREATE TABLE gocoin_accounts(rider_id UUID PRIMARY KEY REFERENCES users(id),balance BIGINT NOT NULL DEFAULT 0 CHECK(balance>=0));
CREATE TABLE gocoin_entries(
 id UUID PRIMARY KEY, rider_id UUID NOT NULL REFERENCES gocoin_accounts(rider_id),
 source_key TEXT NOT NULL UNIQUE, delta BIGINT NOT NULL CHECK(delta<>0), balance_after BIGINT NOT NULL CHECK(balance_after>=0),
 kind TEXT NOT NULL CHECK(kind IN ('ride_reward','redemption')), trip_id UUID REFERENCES trips(id),
 policy_version BIGINT NOT NULL REFERENCES gocoin_policies(version), created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 CHECK((kind='ride_reward' AND delta>0 AND trip_id IS NOT NULL) OR (kind='redemption' AND delta<0 AND trip_id IS NULL))
);
CREATE INDEX gocoin_history ON gocoin_entries(rider_id,created_at DESC,id DESC);
CREATE TRIGGER gocoin_entries_immutable BEFORE UPDATE OR DELETE ON gocoin_entries FOR EACH ROW EXECUTE FUNCTION heroes_immutable_record();
CREATE TABLE gocoin_redemptions(
 rider_id UUID NOT NULL REFERENCES users(id), request_id UUID NOT NULL, coins BIGINT NOT NULL CHECK(coins>0),
 credit_kobo BIGINT NOT NULL CHECK(credit_kobo>0), policy_version BIGINT NOT NULL REFERENCES gocoin_policies(version),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(rider_id,request_id)
);
CREATE TRIGGER gocoin_redemptions_immutable BEFORE UPDATE OR DELETE ON gocoin_redemptions FOR EACH ROW EXECUTE FUNCTION heroes_immutable_record();
ALTER TABLE rider_credit_entries DROP CONSTRAINT rider_credit_entries_kind_check;
ALTER TABLE rider_credit_entries DROP CONSTRAINT rider_credit_entries_check;
ALTER TABLE rider_credit_entries ADD CHECK(kind IN ('referral_reward','ride_payment','gocoin_redemption'));
ALTER TABLE rider_credit_entries ADD CHECK((kind IN ('referral_reward','gocoin_redemption') AND delta_kobo>0 AND trip_id IS NULL) OR (kind='ride_payment' AND delta_kobo<0 AND trip_id IS NOT NULL));
CREATE INDEX rider_credit_history ON rider_credit_entries(rider_id,created_at DESC,id DESC);
ALTER TABLE ride_fares ADD COLUMN gocoin_policy_version BIGINT REFERENCES gocoin_policies(version), ADD COLUMN gocoin_earn_coins BIGINT NOT NULL DEFAULT 0 CHECK(gocoin_earn_coins>=0);
ALTER TABLE trips ADD COLUMN gocoin_policy_version BIGINT REFERENCES gocoin_policies(version), ADD COLUMN gocoin_earn_coins BIGINT NOT NULL DEFAULT 0 CHECK(gocoin_earn_coins>=0);
CREATE FUNCTION snapshot_fare_gocoin() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p gocoin_policies; BEGIN
 SELECT * INTO p FROM gocoin_policies ORDER BY version DESC LIMIT 1;
 NEW.gocoin_policy_version:=p.version;
 NEW.gocoin_earn_coins:=CASE WHEN p.earning_enabled AND COALESCE((p.categories->NEW.package_slug->>'enabled')::boolean,false) THEN COALESCE((p.categories->NEW.package_slug->>'coinsPerRide')::bigint,0) ELSE 0 END;
 RETURN NEW; END $$;
CREATE TRIGGER fare_gocoin_snapshot BEFORE INSERT ON ride_fares FOR EACH ROW EXECUTE FUNCTION snapshot_fare_gocoin();
CREATE FUNCTION snapshot_trip_gocoin() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 SELECT gocoin_policy_version,gocoin_earn_coins INTO NEW.gocoin_policy_version,NEW.gocoin_earn_coins FROM ride_fares WHERE id=NEW.ride_fare_id;
 RETURN NEW; END $$;
CREATE TRIGGER trip_gocoin_snapshot BEFORE INSERT ON trips FOR EACH ROW EXECUTE FUNCTION snapshot_trip_gocoin();
-- Both completion and payment may happen first. The unique source and account
-- lock make either ordering and repeated settlement notifications safe.
CREATE FUNCTION award_trip_gocoin() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE t trips; tid UUID; b BIGINT; BEGIN
 IF TG_TABLE_NAME='trips' THEN tid:=NEW.id; ELSE tid:=NEW.trip_id; END IF;
 SELECT * INTO t FROM trips WHERE id=tid;
 IF t.status<>'completed' OR t.gocoin_earn_coins<=0 OR t.rider_id=t.assigned_driver_id OR NOT EXISTS(SELECT 1 FROM trip_settlements WHERE trip_id=tid AND status='confirmed') OR NOT EXISTS(SELECT 1 FROM users WHERE id=t.rider_id AND active) OR EXISTS(SELECT 1 FROM simulation_participants WHERE actor_user_id IN(t.rider_id,t.assigned_driver_id)) THEN RETURN NEW; END IF;
 INSERT INTO gocoin_accounts(rider_id) VALUES(t.rider_id) ON CONFLICT DO NOTHING;
 SELECT balance INTO b FROM gocoin_accounts WHERE rider_id=t.rider_id FOR UPDATE;
 IF EXISTS(SELECT 1 FROM gocoin_entries WHERE source_key='trip:'||tid) THEN RETURN NEW; END IF;
 b:=b+t.gocoin_earn_coins;
 INSERT INTO gocoin_entries(id,rider_id,source_key,delta,balance_after,kind,trip_id,policy_version) VALUES(gen_random_uuid(),t.rider_id,'trip:'||tid,t.gocoin_earn_coins,b,'ride_reward',tid,t.gocoin_policy_version);
 UPDATE gocoin_accounts SET balance=b WHERE rider_id=t.rider_id;
 RETURN NEW; END $$;
CREATE TRIGGER settlement_gocoin AFTER INSERT OR UPDATE OF status ON trip_settlements FOR EACH ROW EXECUTE FUNCTION award_trip_gocoin();
CREATE TRIGGER completed_gocoin AFTER UPDATE OF status ON trips FOR EACH ROW WHEN(NEW.status='completed') EXECUTE FUNCTION award_trip_gocoin();
