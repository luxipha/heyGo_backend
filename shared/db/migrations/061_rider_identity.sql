CREATE TABLE rider_profiles (
    rider_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    display_name TEXT NOT NULL CHECK (length(display_name) BETWEEN 1 AND 100),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- These proofs cannot be used for sign-in or another account/purpose.
CREATE TABLE rider_contact_challenges (
    id UUID PRIMARY KEY,
    rider_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    purpose TEXT NOT NULL CHECK (purpose IN ('phone_change','email_link')),
    current_destination TEXT NOT NULL,
    new_destination TEXT NOT NULL,
    current_code_hash TEXT,
    new_code_hash TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX rider_contact_challenge_rate_idx ON rider_contact_challenges(rider_id,created_at DESC);
