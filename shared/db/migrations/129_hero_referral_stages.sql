-- Existing referrals retain their single-reward snapshot. Stages are captured only on new claims.
CREATE TABLE hero_referral_stages (
 id UUID PRIMARY KEY, referral_id UUID NOT NULL REFERENCES referral_attributions(id),
 position INT NOT NULL CHECK(position>0), trip_target INT NOT NULL CHECK(trip_target BETWEEN 1 AND 10000),
 reward_kobo BIGINT NOT NULL CHECK(reward_kobo BETWEEN 1 AND 100000000),
 approval_mode TEXT NOT NULL CHECK(approval_mode IN ('manual','automatic')),
 qualified_at TIMESTAMPTZ, credited_at TIMESTAMPTZ, approved_by UUID REFERENCES admin_users(id),
 UNIQUE(referral_id,position), UNIQUE(referral_id,trip_target),
 CHECK(credited_at IS NULL OR qualified_at IS NOT NULL),
 CHECK(approved_by IS NULL OR credited_at IS NOT NULL),
 CHECK(credited_at IS NULL OR approval_mode='automatic' OR approved_by IS NOT NULL)
);
CREATE INDEX hero_stages_pending ON hero_referral_stages(referral_id,position) WHERE credited_at IS NULL;
CREATE FUNCTION hero_stage_protect() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Stage history is permanent'; END IF;
 IF (to_jsonb(NEW)-ARRAY['qualified_at','credited_at','approved_by']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['qualified_at','credited_at','approved_by']) OR
 (OLD.qualified_at IS NOT NULL AND NEW.qualified_at IS DISTINCT FROM OLD.qualified_at) OR
 (OLD.credited_at IS NOT NULL AND NEW IS DISTINCT FROM OLD) THEN RAISE EXCEPTION 'Stage rules and credits are immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hero_stage_immutable BEFORE UPDATE OR DELETE ON hero_referral_stages FOR EACH ROW EXECUTE FUNCTION hero_stage_protect();
ALTER TABLE heroes_wallet_entries ADD COLUMN stage_id UUID UNIQUE REFERENCES hero_referral_stages(id);
ALTER TABLE heroes_wallet_entries DROP CONSTRAINT heroes_wallet_one_referral;
ALTER TABLE heroes_wallet_entries ADD CONSTRAINT heroes_wallet_one_referral CHECK(num_nonnulls(referral_id,programme_referral_id,stage_id)=1);
CREATE TABLE hero_stage_payment_allocations (
 stage_id UUID NOT NULL REFERENCES hero_referral_stages(id),
 payment_id UUID NOT NULL REFERENCES heroes_manual_payments(id),
 PRIMARY KEY(stage_id,payment_id)
);
CREATE TRIGGER hero_stage_payment_immutable BEFORE UPDATE OR DELETE ON hero_stage_payment_allocations FOR EACH ROW EXECUTE FUNCTION heroes_immutable_record();
-- Expose staged reward totals through existing Hero overview/wallet queries.
CREATE OR REPLACE VIEW heroes_all_referrals AS
 SELECT id,hero_id,driver_id,policy_id,claimed_at,onboarded_at,qualified_at FROM heroes_referrals
 UNION ALL
 SELECT a.id,a.hero_id,a.invitee_id,a.id,a.linked_at,a.onboarded_at,
 CASE WHEN EXISTS(SELECT 1 FROM hero_referral_stages s WHERE s.referral_id=a.id)
 THEN (SELECT max(s.credited_at) FROM hero_referral_stages s WHERE s.referral_id=a.id HAVING bool_and(s.credited_at IS NOT NULL))
 ELSE a.credited_at END
 FROM referral_attributions a WHERE a.inviter_role='hero';
ALTER TABLE hero_referral_stages ADD COLUMN payout_mode TEXT NOT NULL DEFAULT 'manual' CHECK(payout_mode IN ('manual','monnify'));
ALTER TABLE heroes_bank_accounts ADD COLUMN bank_code TEXT NOT NULL DEFAULT '';
ALTER TABLE heroes_bank_accounts ADD COLUMN verified_at TIMESTAMPTZ;
ALTER TABLE heroes_manual_payments ALTER COLUMN recorded_by DROP NOT NULL;
ALTER TABLE heroes_manual_payments ADD COLUMN method TEXT NOT NULL DEFAULT 'manual' CHECK(method IN ('manual','monnify'));
ALTER TABLE heroes_manual_payments ADD CONSTRAINT hero_manual_payment_actor CHECK(method='monnify' OR recorded_by IS NOT NULL);
CREATE TABLE hero_stage_payouts (
 id UUID PRIMARY KEY, stage_id UUID NOT NULL REFERENCES hero_referral_stages(id), hero_id UUID NOT NULL REFERENCES heroes_accounts(id),
 bank_account_id UUID NOT NULL REFERENCES heroes_bank_accounts(id), amount_kobo BIGINT NOT NULL CHECK(amount_kobo>0),
 reference TEXT NOT NULL UNIQUE, monnify_config_version BIGINT REFERENCES admin_monnify_settings(version),
 status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','submitting','uncertain','PENDING','AWAITING_PROCESSING','IN_PROGRESS','PENDING_AUTHORIZATION','OTP_EMAIL_DISPATCH_FAILED','SUCCESS','COMPLETED','FAILED','EXPIRED','REVERSED','CANCELLED')),
 transaction_reference TEXT, requested_by UUID REFERENCES admin_users(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX hero_stage_active_payout ON hero_stage_payouts(stage_id) WHERE status NOT IN ('FAILED','EXPIRED','REVERSED','CANCELLED');
CREATE INDEX hero_stage_payout_pending ON hero_stage_payouts(updated_at) WHERE status NOT IN ('FAILED','EXPIRED','REVERSED','CANCELLED');
ALTER TABLE heroes_payment_reversals ALTER COLUMN recorded_by DROP NOT NULL;
ALTER TABLE admin_monnify_settings ADD COLUMN disbursement_source_ciphertext BYTEA;
