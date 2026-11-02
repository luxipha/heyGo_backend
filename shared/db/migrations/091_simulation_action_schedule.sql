ALTER TABLE simulation_rider_actions ADD COLUMN scheduled_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
CREATE INDEX simulation_actions_schedule ON simulation_rider_actions(simulation_run_id,scheduled_at,created_at) WHERE status='queued';
