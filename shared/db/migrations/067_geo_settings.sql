CREATE TABLE admin_geo_settings (
 id INTEGER PRIMARY KEY CHECK(id=1),
 configuration JSONB NOT NULL,
 version BIGINT NOT NULL DEFAULT 1,
 updated_by UUID REFERENCES admin_users(id),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
