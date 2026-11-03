-- Heroes is isolated from Rider, Driver and staff identities for future extraction.
CREATE TABLE heroes_accounts (
 id UUID PRIMARY KEY, email TEXT NOT NULL UNIQUE CHECK(email=lower(email)), name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 100),
 password_hash TEXT NOT NULL, referral_code TEXT NOT NULL UNIQUE, active BOOLEAN NOT NULL DEFAULT TRUE,
 failed_attempts INT NOT NULL DEFAULT 0, locked_until TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE heroes_sessions (token_hash TEXT PRIMARY KEY, hero_id UUID NOT NULL REFERENCES heroes_accounts(id), expires_at TIMESTAMPTZ NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE INDEX heroes_sessions_owner ON heroes_sessions(hero_id);
CREATE TABLE heroes_password_resets(token_hash TEXT PRIMARY KEY,hero_id UUID NOT NULL REFERENCES heroes_accounts(id),expires_at TIMESTAMPTZ NOT NULL,used_at TIMESTAMPTZ);
CREATE TABLE heroes_policies (
 id UUID PRIMARY KEY, version BIGSERIAL UNIQUE, enabled BOOLEAN NOT NULL DEFAULT FALSE,
 trip_target INT NOT NULL CHECK(trip_target BETWEEN 1 AND 10000),reward_kobo BIGINT NOT NULL CHECK(reward_kobo BETWEEN 0 AND 100000000),
 ranks JSONB NOT NULL DEFAULT '{"E":0}',created_by UUID REFERENCES admin_users(id),created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO heroes_policies(id,trip_target,reward_kobo)VALUES(gen_random_uuid(),70,0);
CREATE TABLE heroes_driver_leads (
 id UUID PRIMARY KEY,hero_id UUID NOT NULL REFERENCES heroes_accounts(id),name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 100),phone TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),UNIQUE(hero_id,phone)
);
CREATE TABLE heroes_referrals (
 id UUID PRIMARY KEY,hero_id UUID NOT NULL REFERENCES heroes_accounts(id),driver_id UUID NOT NULL UNIQUE REFERENCES users(id),
 policy_id UUID NOT NULL REFERENCES heroes_policies(id),claimed_at TIMESTAMPTZ NOT NULL DEFAULT now(),onboarded_at TIMESTAMPTZ,qualified_at TIMESTAMPTZ
);
CREATE INDEX heroes_referrals_owner ON heroes_referrals(hero_id);
CREATE TABLE heroes_wallet_entries (
 id UUID PRIMARY KEY,hero_id UUID NOT NULL REFERENCES heroes_accounts(id),referral_id UUID NOT NULL UNIQUE REFERENCES heroes_referrals(id),
 amount_kobo BIGINT NOT NULL CHECK(amount_kobo>0),currency TEXT NOT NULL DEFAULT 'NGN' CHECK(currency='NGN'),
 kind TEXT NOT NULL DEFAULT 'referral_reward' CHECK(kind='referral_reward'),created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE FUNCTION heroes_immutable_record() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'Heroes reward policies and wallet entries are immutable'; END $$;
CREATE TRIGGER heroes_wallet_immutable BEFORE UPDATE OR DELETE ON heroes_wallet_entries FOR EACH ROW EXECUTE FUNCTION heroes_immutable_record();
CREATE TRIGGER heroes_policy_immutable BEFORE UPDATE OR DELETE ON heroes_policies FOR EACH ROW EXECUTE FUNCTION heroes_immutable_record();
