ALTER TABLE driver_devices ADD COLUMN session_id UUID REFERENCES user_sessions(id) ON DELETE CASCADE;
CREATE INDEX driver_devices_session_idx ON driver_devices(session_id);
ALTER TABLE driver_socket_sessions ADD COLUMN auth_session_id UUID REFERENCES user_sessions(id) ON DELETE CASCADE;
