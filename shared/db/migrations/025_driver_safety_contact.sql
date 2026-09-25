CREATE TABLE IF NOT EXISTS driver_safety_contacts (
    driver_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    full_name TEXT NOT NULL CHECK (char_length(full_name) BETWEEN 1 AND 120),
    relationship TEXT NOT NULL CHECK (char_length(relationship) BETWEEN 1 AND 80),
    phone_e164 TEXT NOT NULL CHECK (phone_e164 ~ '^\+[1-9][0-9]{7,14}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
