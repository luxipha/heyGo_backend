CREATE TABLE IF NOT EXISTS admin_users (
    id UUID PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    failed_attempts INTEGER NOT NULL DEFAULT 0,
    locked_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT admin_username_normalized CHECK (username = LOWER(username))
);

CREATE TABLE IF NOT EXISTS admin_sessions (
    token_hash TEXT PRIMARY KEY,
    admin_id UUID NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS admin_sessions_admin_idx ON admin_sessions(admin_id, expires_at DESC);

CREATE TABLE IF NOT EXISTS admin_audit (
    id UUID PRIMARY KEY,
    admin_id UUID NOT NULL REFERENCES admin_users(id),
    action TEXT NOT NULL,
    target_driver_id UUID REFERENCES users(id),
    details JSONB NOT NULL DEFAULT '{}'::JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS admin_audit_driver_idx ON admin_audit(target_driver_id, created_at DESC);

ALTER TABLE driver_documents ADD COLUMN IF NOT EXISTS reviewed_by UUID REFERENCES admin_users(id);
ALTER TABLE driver_inspections ADD CONSTRAINT driver_inspections_reviewer_fk FOREIGN KEY (reviewed_by) REFERENCES admin_users(id);

CREATE TABLE IF NOT EXISTS admin_report_uploads (
    id UUID PRIMARY KEY,
    admin_id UUID NOT NULL REFERENCES admin_users(id),
    driver_id UUID NOT NULL REFERENCES users(id),
    storage_key TEXT NOT NULL UNIQUE,
    content_type TEXT NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes > 0),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
