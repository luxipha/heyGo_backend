-- Existing accounts never become eligible to add an incoming referral.
ALTER TABLE users ADD COLUMN referral_signup_open BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE users ALTER COLUMN referral_signup_open SET DEFAULT TRUE;
CREATE TABLE referral_settings (
 version BIGSERIAL PRIMARY KEY, enabled BOOLEAN NOT NULL DEFAULT FALSE,
 disabled_notice TEXT NOT NULL CHECK(length(disabled_notice) BETWEEN 1 AND 240),
 driver_headline TEXT NOT NULL CHECK(length(driver_headline) BETWEEN 1 AND 120),
 driver_body TEXT NOT NULL CHECK(length(driver_body)<=240),
 rider_headline TEXT NOT NULL CHECK(length(rider_headline) BETWEEN 1 AND 120),
 rider_body TEXT NOT NULL CHECK(length(rider_body)<=240),
 rules JSONB NOT NULL,
 created_by UUID REFERENCES admin_users(id), created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO referral_settings(disabled_notice,driver_headline,driver_body,rider_headline,rider_body,rules)
VALUES('Referrals are currently paused.','Invite & earn','Share your code. Track your rewards.',
'Invite friends to ride','Share your code. Track your rewards.',
'{"hero_driver":{"rewardKobo":0,"tripTarget":30,"deadlineDays":null},"driver_driver":{"rewardKobo":0,"tripTarget":30,"deadlineDays":null},"driver_rider":{"rewardKobo":0,"tripTarget":1,"deadlineDays":null},"rider_rider":{"rewardKobo":0,"tripTarget":1,"deadlineDays":null}}');
CREATE TRIGGER referral_settings_immutable BEFORE UPDATE OR DELETE ON referral_settings FOR EACH ROW EXECUTE FUNCTION heroes_immutable_record();
CREATE TABLE referral_codes (
 user_id UUID NOT NULL REFERENCES users(id), code TEXT NOT NULL UNIQUE,
 inviter_role TEXT NOT NULL CHECK(inviter_role IN ('driver','rider')), created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(user_id,inviter_role)
);
CREATE TABLE referral_attributions (
 id UUID PRIMARY KEY, invitee_id UUID NOT NULL UNIQUE REFERENCES users(id),
 inviter_id UUID REFERENCES users(id), hero_id UUID REFERENCES heroes_accounts(id),
 inviter_role TEXT NOT NULL CHECK(inviter_role IN ('hero','driver','rider')),
 invitee_role TEXT NOT NULL CHECK(invitee_role IN ('driver','rider')),
 settings_version BIGINT NOT NULL REFERENCES referral_settings(version),
 reward_kobo BIGINT NOT NULL CHECK(reward_kobo BETWEEN 0 AND 100000000),
 trip_target INT NOT NULL CHECK(trip_target BETWEEN 1 AND 10000), deadline_at TIMESTAMPTZ,
 linked_at TIMESTAMPTZ NOT NULL DEFAULT now(), onboarded_at TIMESTAMPTZ, qualified_at TIMESTAMPTZ,
 credited_at TIMESTAMPTZ, approved_by UUID REFERENCES admin_users(id),
 CHECK ((inviter_role='hero' AND hero_id IS NOT NULL AND inviter_id IS NULL AND invitee_role='driver') OR
 (inviter_role IN ('driver','rider') AND inviter_id IS NOT NULL AND hero_id IS NULL)),
 CHECK(inviter_id IS DISTINCT FROM invitee_id),
 CHECK(inviter_role<>'rider' OR invitee_role='rider'),
 CHECK((credited_at IS NULL)=(approved_by IS NULL))
);
CREATE INDEX referral_attributions_owner ON referral_attributions(inviter_id,linked_at DESC,id DESC);
CREATE INDEX referral_pending_rewards ON referral_attributions(qualified_at,id) WHERE credited_at IS NULL;
-- This shared guard also protects legacy Hero attribution from racing a new claim.
CREATE FUNCTION referral_single_referrer() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE actor UUID;
BEGIN
 IF TG_TABLE_NAME='heroes_referrals' THEN actor:=NEW.driver_id; ELSE actor:=NEW.invitee_id; END IF;
 PERFORM pg_advisory_xact_lock(hashtext('referral-user:'||actor::text));
 IF TG_TABLE_NAME='heroes_referrals' THEN
  IF EXISTS(SELECT 1 FROM referral_attributions WHERE invitee_id=actor) THEN RAISE EXCEPTION 'Referrer already confirmed'; END IF;
 ELSE
  IF EXISTS(SELECT 1 FROM heroes_referrals WHERE driver_id=actor) THEN RAISE EXCEPTION 'Referrer already confirmed'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER referral_single_new BEFORE INSERT ON referral_attributions FOR EACH ROW EXECUTE FUNCTION referral_single_referrer();
CREATE TRIGGER referral_single_legacy BEFORE INSERT ON heroes_referrals FOR EACH ROW EXECUTE FUNCTION referral_single_referrer();
CREATE FUNCTION referral_attribution_protect() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Referral attribution is permanent'; END IF;
 IF (to_jsonb(NEW)-ARRAY['onboarded_at','qualified_at','credited_at','approved_by']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['onboarded_at','qualified_at','credited_at','approved_by']) OR
 (OLD.onboarded_at IS NOT NULL AND NEW.onboarded_at IS DISTINCT FROM OLD.onboarded_at) OR
 (OLD.qualified_at IS NOT NULL AND NEW.qualified_at IS DISTINCT FROM OLD.qualified_at) OR
 (OLD.credited_at IS NOT NULL AND NEW IS DISTINCT FROM OLD) THEN RAISE EXCEPTION 'Referral attribution and credited rewards are immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER referral_attribution_immutable BEFORE UPDATE OR DELETE ON referral_attributions FOR EACH ROW EXECUTE FUNCTION referral_attribution_protect();
CREATE TABLE referral_admin_notifications (
 referral_id UUID PRIMARY KEY REFERENCES referral_attributions(id), created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 read_at TIMESTAMPTZ, read_by UUID REFERENCES admin_users(id)
);
CREATE TABLE rider_credit_accounts (
 rider_id UUID PRIMARY KEY REFERENCES users(id), balance_kobo BIGINT NOT NULL DEFAULT 0 CHECK(balance_kobo>=0), updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE rider_credit_entries (
 id UUID PRIMARY KEY, rider_id UUID NOT NULL REFERENCES rider_credit_accounts(rider_id),
 source_key TEXT NOT NULL UNIQUE, delta_kobo BIGINT NOT NULL CHECK(delta_kobo<>0), balance_after_kobo BIGINT NOT NULL CHECK(balance_after_kobo>=0),
 kind TEXT NOT NULL CHECK(kind IN ('referral_reward','ride_payment')), trip_id UUID REFERENCES trips(id), created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 CHECK((kind='referral_reward' AND delta_kobo>0 AND trip_id IS NULL) OR (kind='ride_payment' AND delta_kobo<0 AND trip_id IS NOT NULL))
);
CREATE TRIGGER rider_credit_entries_immutable BEFORE UPDATE OR DELETE ON rider_credit_entries FOR EACH ROW EXECUTE FUNCTION heroes_immutable_record();
-- Positive referral credits have their own ledger kind; they are not provider top-ups.
ALTER TABLE driver_operating_entries DROP CONSTRAINT driver_operating_entries_kind_check;
ALTER TABLE driver_operating_entries ADD CONSTRAINT driver_operating_entries_kind_check CHECK(kind IN ('topup','statutory_charge','reversal','no_show_transfer','rider_credit_payment','referral_reward'));
ALTER TABLE driver_operating_entries DROP CONSTRAINT driver_operating_entries_sign_check;
ALTER TABLE driver_operating_entries ADD CONSTRAINT driver_operating_entries_check CHECK((kind IN ('topup','referral_reward','rider_credit_payment') AND delta_kobo>0) OR (kind='statutory_charge' AND delta_kobo<0) OR kind IN ('reversal','no_show_transfer'));

ALTER TABLE heroes_wallet_entries ALTER COLUMN referral_id DROP NOT NULL;
ALTER TABLE heroes_wallet_entries ADD COLUMN programme_referral_id UUID UNIQUE REFERENCES referral_attributions(id);
ALTER TABLE heroes_wallet_entries ADD CONSTRAINT heroes_wallet_one_referral CHECK((referral_id IS NULL)<>(programme_referral_id IS NULL));
CREATE VIEW heroes_all_referrals AS
 SELECT id,hero_id,driver_id,policy_id,claimed_at,onboarded_at,qualified_at FROM heroes_referrals
 UNION ALL
 SELECT a.id,a.hero_id,a.invitee_id,a.id,a.linked_at,
 a.onboarded_at,a.credited_at
 FROM referral_attributions a WHERE a.inviter_role='hero';

CREATE VIEW heroes_all_policies AS
 SELECT id,version,enabled,trip_target,reward_kobo,ranks,created_by,created_at FROM heroes_policies
 UNION ALL
 SELECT a.id,a.settings_version,TRUE,a.trip_target,a.reward_kobo,'{"E":0}'::jsonb,NULL::uuid,a.linked_at FROM referral_attributions a WHERE a.inviter_role='hero';

ALTER TABLE trips ADD COLUMN ride_credit_kobo BIGINT NOT NULL DEFAULT 0 CHECK(ride_credit_kobo>=0);
ALTER TABLE trip_settlements ADD COLUMN ride_credit_kobo BIGINT NOT NULL DEFAULT 0 CHECK(ride_credit_kobo>=0);
ALTER TABLE trip_settlements DROP CONSTRAINT trip_settlements_expected_amount_kobo_check;
ALTER TABLE trip_settlements ADD CONSTRAINT trip_settlements_expected_amount_kobo_check CHECK(expected_amount_kobo>=0);
