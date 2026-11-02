CREATE TABLE simulation_commands (
 id UUID PRIMARY KEY, simulation_run_id UUID NOT NULL REFERENCES simulation_runs(id),
 participant_id UUID NOT NULL REFERENCES simulation_participants(id),
 trip_id UUID NOT NULL REFERENCES trips(id),
 action TEXT NOT NULL CHECK(action IN ('accept','decline')),
 status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','sent','failed')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), processed_at TIMESTAMPTZ,
 UNIQUE(simulation_run_id,id)
);
CREATE INDEX simulation_commands_pending ON simulation_commands(simulation_run_id,created_at) WHERE status='queued';
