-- Socket ownership is shared across gateway replicas and expires after a crash.
CREATE TABLE IF NOT EXISTS driver_socket_sessions (
    driver_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    session_id UUID NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS driver_socket_sessions_expiry_idx ON driver_socket_sessions(expires_at);
