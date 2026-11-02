CREATE TABLE IF NOT EXISTS admin_agent_settings (
 id INTEGER PRIMARY KEY CHECK(id=1), configuration JSONB NOT NULL,
 version BIGINT NOT NULL DEFAULT 1, updated_by UUID REFERENCES admin_users(id),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS agent_daily_usage (
 day DATE PRIMARY KEY, calls INTEGER NOT NULL CHECK(calls>=0)
);
