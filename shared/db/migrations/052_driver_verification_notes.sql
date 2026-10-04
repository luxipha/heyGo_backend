CREATE TABLE IF NOT EXISTS driver_verification_notes (
    id UUID PRIMARY KEY,
    driver_id UUID NOT NULL REFERENCES driver_profiles(driver_id) ON DELETE CASCADE,
    admin_id UUID NOT NULL REFERENCES admin_users(id),
    body TEXT NOT NULL CHECK (char_length(trim(body)) BETWEEN 1 AND 2000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS driver_verification_notes_driver_idx
    ON driver_verification_notes(driver_id, created_at DESC, id DESC);
