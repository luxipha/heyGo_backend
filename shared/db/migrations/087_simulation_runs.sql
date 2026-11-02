-- Configuration/audit foundation only. No actors are started by migration.
CREATE TABLE simulation_runs (
 id UUID PRIMARY KEY, name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 120),
 is_simulated BOOLEAN NOT NULL DEFAULT TRUE CHECK(is_simulated),
 seed BIGINT NOT NULL CHECK(seed>=0), status TEXT NOT NULL DEFAULT 'draft'
 CHECK(status IN ('draft','starting','running','paused','stopping','stopped','failed','completed')),
 revision BIGINT NOT NULL DEFAULT 1, created_by UUID NOT NULL REFERENCES admin_users(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE simulation_participants (
 id UUID PRIMARY KEY, simulation_run_id UUID NOT NULL REFERENCES simulation_runs(id),
 is_simulated BOOLEAN NOT NULL DEFAULT TRUE CHECK(is_simulated),
 role TEXT NOT NULL CHECK(role IN ('driver','rider')), name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 100),
 configuration JSONB NOT NULL, controller TEXT NOT NULL DEFAULT 'simulator' CHECK(controller IN ('simulator','app')),
 actor_user_id UUID UNIQUE REFERENCES users(id), runtime_status TEXT NOT NULL DEFAULT 'configured',
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX simulation_participants_run ON simulation_participants(simulation_run_id,created_at,id);
CREATE TABLE simulation_events (
 id BIGSERIAL PRIMARY KEY, simulation_run_id UUID NOT NULL REFERENCES simulation_runs(id),
 participant_id UUID REFERENCES simulation_participants(id), kind TEXT NOT NULL,
 data JSONB NOT NULL, recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX simulation_events_run ON simulation_events(simulation_run_id,id);
