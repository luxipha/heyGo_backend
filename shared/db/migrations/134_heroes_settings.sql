-- Single-row switch for whether new Heroes may sign up. Existing Heroes are never affected.
CREATE TABLE heroes_settings (
 id SMALLINT PRIMARY KEY CHECK(id=1),
 accepting_new BOOLEAN NOT NULL DEFAULT TRUE,
 updated_by UUID REFERENCES admin_users(id),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO heroes_settings(id) VALUES(1);
