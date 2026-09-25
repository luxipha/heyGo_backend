ALTER TABLE driver_data_exports ADD COLUMN IF NOT EXISTS archive_link_ciphertext BYTEA;
ALTER TABLE driver_data_exports ADD COLUMN IF NOT EXISTS mail_from_email TEXT;
ALTER TABLE driver_data_exports ADD COLUMN IF NOT EXISTS mail_from_name TEXT;
ALTER TABLE driver_data_exports ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
