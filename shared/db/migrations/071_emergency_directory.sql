CREATE TABLE IF NOT EXISTS admin_emergency_directory (
 id SMALLINT PRIMARY KEY CHECK(id=1), contacts JSONB NOT NULL DEFAULT '[]', version BIGINT NOT NULL DEFAULT 1,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS driver_additional_safety_contacts (
 id UUID PRIMARY KEY, driver_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 full_name TEXT NOT NULL CHECK(char_length(full_name) BETWEEN 1 AND 120),
 relationship TEXT NOT NULL CHECK(char_length(relationship) BETWEEN 1 AND 80),
 phone_e164 TEXT NOT NULL CHECK(phone_e164 ~ '^\+[1-9][0-9]{7,14}$'),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS driver_additional_safety_contacts_owner ON driver_additional_safety_contacts(driver_id,created_at);
