CREATE TABLE simulation_rider_actions (
 id UUID PRIMARY KEY, simulation_run_id UUID NOT NULL REFERENCES simulation_runs(id),
 participant_id UUID NOT NULL REFERENCES simulation_participants(id),
 action TEXT NOT NULL CHECK(action IN ('search','preview','book','cancel','receipt')),
 payload JSONB NOT NULL, result JSONB, error TEXT,
 status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','processing','succeeded','failed')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),processed_at TIMESTAMPTZ
);
CREATE INDEX simulation_rider_actions_pending ON simulation_rider_actions(simulation_run_id,created_at) WHERE status='queued';
