ALTER TABLE users
    ADD COLUMN IF NOT EXISTS provider_trust_score INTEGER,
    ADD COLUMN IF NOT EXISTS consumer_trust_score INTEGER,
    ADD COLUMN IF NOT EXISTS provider_trust_status TEXT,
    ADD COLUMN IF NOT EXISTS consumer_trust_status TEXT,
    ADD COLUMN IF NOT EXISTS trust_score_updated_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS trust_score_policy_version TEXT;

ALTER TABLE users DROP COLUMN IF EXISTS trust_score;

CREATE INDEX IF NOT EXISTS users_provider_trust_idx ON users(provider_trust_score DESC NULLS LAST);
