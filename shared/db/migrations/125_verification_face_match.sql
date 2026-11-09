-- Staff answer to "Is the person in the selfie the same as on the ID photo?".
-- The answer is tied to the exact selfie and ID documents it was given for;
-- if the driver uploads either again, the answer no longer applies.
CREATE TABLE driver_face_match_reviews (
  driver_id UUID PRIMARY KEY REFERENCES users(id),
  result TEXT NOT NULL CHECK (result IN ('match', 'no_match', 'unsure')),
  selfie_document_id UUID NOT NULL,
  id_document_id UUID NOT NULL,
  reviewed_by UUID NOT NULL REFERENCES admin_users(id),
  reviewed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Queue "oldest first" pages by application time.
CREATE INDEX IF NOT EXISTS driver_profiles_created_idx ON driver_profiles (created_at, driver_id);
