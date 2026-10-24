ALTER TABLE driver_identity_profiles ADD COLUMN residential_address TEXT NOT NULL DEFAULT '' CHECK (char_length(residential_address)<=1000);
