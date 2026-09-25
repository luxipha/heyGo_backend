CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY,
    casper_id_user_id TEXT NOT NULL UNIQUE,
    human_id TEXT NOT NULL UNIQUE,
    roles TEXT[] NOT NULL DEFAULT '{}',
    verified BOOLEAN NOT NULL DEFAULT FALSE,
    kyc_tier TEXT,
    trust_score INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT users_roles_allowed CHECK (roles <@ ARRAY['rider', 'driver']::TEXT[])
);

CREATE INDEX IF NOT EXISTS users_roles_gin ON users USING GIN (roles);
