ALTER TABLE auth_otp_challenges ADD COLUMN purpose TEXT NOT NULL DEFAULT 'login' CHECK (purpose IN ('login','pin_reset'));
ALTER TABLE auth_otp_challenges ADD COLUMN pin_reset_user_id UUID REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE driver_devices ADD COLUMN revoked_at TIMESTAMPTZ;
