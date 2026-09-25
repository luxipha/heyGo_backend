CREATE TABLE IF NOT EXISTS driver_devices (
    id UUID PRIMARY KEY,
    driver_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    platform TEXT NOT NULL CHECK (platform IN ('ios', 'android')),
    push_token TEXT NOT NULL,
    app_version TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(driver_id, push_token)
);
CREATE INDEX IF NOT EXISTS driver_devices_driver_idx ON driver_devices(driver_id, updated_at DESC);
