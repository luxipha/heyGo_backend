CREATE TABLE rider_saved_places (
 id UUID PRIMARY KEY,
 rider_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 label TEXT NOT NULL CHECK(length(label) BETWEEN 1 AND 80),
 label_key TEXT NOT NULL,
 provider TEXT NOT NULL,
 place_id TEXT NOT NULL,
 version BIGINT NOT NULL DEFAULT 1,
 verified_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 UNIQUE(rider_id,label_key)
);
-- Only stable identifiers are retained; provider metadata is resolved on demand.
CREATE TABLE rider_trusted_contacts (
 id UUID PRIMARY KEY,
 rider_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 full_name TEXT NOT NULL,
 relationship TEXT NOT NULL,
 phone_e164 TEXT NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX rider_trusted_contacts_owner_idx ON rider_trusted_contacts(rider_id,id);
CREATE TABLE rider_preferences (
 rider_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 reduce_motion BOOLEAN NOT NULL DEFAULT FALSE,
 share_ride_status BOOLEAN NOT NULL DEFAULT FALSE,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- Reuse the existing durable private-export and account-wide review machinery.
-- Legacy table names/driver_id remain for compatibility; driver_id is a users FK.
CREATE TABLE rider_data_exports (LIKE driver_data_exports INCLUDING ALL);
ALTER TABLE rider_data_exports RENAME COLUMN driver_id TO rider_id;
ALTER TABLE rider_data_exports ADD FOREIGN KEY(rider_id) REFERENCES users(id);
ALTER TABLE driver_account_deletion_requests ADD COLUMN requester_role TEXT NOT NULL DEFAULT 'driver' CHECK(requester_role IN ('driver','rider'));
