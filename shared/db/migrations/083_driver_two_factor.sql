ALTER TABLE users ADD COLUMN driver_two_factor_enabled BOOLEAN NOT NULL DEFAULT FALSE;
CREATE TABLE auth_pin_challenges (
 token_hash TEXT PRIMARY KEY,
 user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 device_name TEXT NOT NULL DEFAULT '',
 expires_at TIMESTAMPTZ NOT NULL,
 consumed_at TIMESTAMPTZ,
 review_otp_version BIGINT,
 review_expires_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX auth_pin_challenges_expiry_idx ON auth_pin_challenges(expires_at);
