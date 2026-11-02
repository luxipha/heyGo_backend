-- Unknown historical state stays unknown until supplied by Driver or staff.
ALTER TABLE driver_identity_profiles ADD COLUMN IF NOT EXISTS residential_state TEXT NOT NULL DEFAULT '';
