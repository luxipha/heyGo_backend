-- Connection state and matching state are separate from the driver's explicit
-- online choice. An offer temporarily makes available=false without clearing it.
ALTER TABLE drivers ADD COLUMN IF NOT EXISTS online_requested BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE drivers SET status='offline', available=FALSE, updated_at=NOW() WHERE status<>'on_trip';
