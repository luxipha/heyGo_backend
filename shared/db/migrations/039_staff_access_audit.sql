ALTER TABLE admin_users ADD COLUMN IF NOT EXISTS email TEXT;
ALTER TABLE admin_users ADD COLUMN IF NOT EXISTS email_verified_at TIMESTAMPTZ;
CREATE UNIQUE INDEX IF NOT EXISTS admin_users_email_unique ON admin_users(LOWER(email)) WHERE email IS NOT NULL;
ALTER TABLE admin_users ADD CONSTRAINT admin_users_email_normalized CHECK (email IS NULL OR email=LOWER(email));

CREATE TABLE IF NOT EXISTS admin_access_tokens (
    token_hash TEXT PRIMARY KEY,
    admin_id UUID NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    email TEXT NOT NULL,
    purpose TEXT NOT NULL CHECK (purpose IN ('invite','recovery','verify_email')),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS admin_access_tokens_admin_idx ON admin_access_tokens(admin_id,purpose,created_at DESC);

ALTER TABLE admin_audit ADD COLUMN IF NOT EXISTS target_type TEXT;
ALTER TABLE admin_audit ADD COLUMN IF NOT EXISTS target_id TEXT;
ALTER TABLE admin_audit ADD COLUMN IF NOT EXISTS before_state JSONB;
ALTER TABLE admin_audit ADD COLUMN IF NOT EXISTS after_state JSONB;
ALTER TABLE admin_audit ADD COLUMN IF NOT EXISTS reason TEXT;
CREATE INDEX IF NOT EXISTS admin_audit_target_idx ON admin_audit(target_type,target_id,created_at DESC);
UPDATE admin_audit SET target_type='driver',target_id=target_driver_id::TEXT
WHERE target_driver_id IS NOT NULL AND target_type IS NULL;
