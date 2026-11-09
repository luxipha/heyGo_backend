CREATE TABLE rider_phone_challenges (
 id UUID PRIMARY KEY,
 rider_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 destination TEXT NOT NULL,
 previous_phone TEXT NOT NULL DEFAULT '',
 code_hash TEXT NOT NULL,
 attempts INT NOT NULL DEFAULT 0,
 expires_at TIMESTAMPTZ NOT NULL,
 consumed_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX rider_phone_challenges_owner ON rider_phone_challenges(rider_id,created_at);
CREATE INDEX rider_phone_challenges_destination ON rider_phone_challenges(destination,created_at);

