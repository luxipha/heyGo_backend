CREATE TABLE IF NOT EXISTS admin_document_uploads (
    id UUID PRIMARY KEY,
    admin_id UUID NOT NULL REFERENCES admin_users(id),
    driver_id UUID NOT NULL REFERENCES driver_profiles(driver_id) ON DELETE CASCADE,
    type TEXT NOT NULL,
    side TEXT NOT NULL CHECK (side IN ('document','front','back')),
    storage_key TEXT NOT NULL UNIQUE,
    content_type TEXT NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes > 0),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS admin_document_uploads_owner_idx ON admin_document_uploads(admin_id,driver_id,created_at DESC);
