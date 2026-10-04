-- Identity review data is separate from operational Driver profile details.
-- Only the final four digits of regulated identifiers are retained until a
-- separately approved verification provider and data-protection flow exists.
CREATE TABLE IF NOT EXISTS driver_identity_profiles (
    driver_id UUID PRIMARY KEY REFERENCES driver_profiles(driver_id) ON DELETE CASCADE,
    first_name TEXT NOT NULL DEFAULT '',
    last_name TEXT NOT NULL DEFAULT '',
    age SMALLINT CHECK (age IS NULL OR age BETWEEN 18 AND 120),
    nin_last4 TEXT NOT NULL DEFAULT '' CHECK (nin_last4 = '' OR nin_last4 ~ '^[0-9]{4}$'),
    bvn_last4 TEXT NOT NULL DEFAULT '' CHECK (bvn_last4 = '' OR bvn_last4 ~ '^[0-9]{4}$'),
    id_type TEXT NOT NULL DEFAULT '',
    id_number_last4 TEXT NOT NULL DEFAULT '' CHECK (id_number_last4 = '' OR id_number_last4 ~ '^[0-9]{4}$'),
    next_of_kin TEXT NOT NULL DEFAULT '',
    relationship TEXT NOT NULL DEFAULT '',
    emergency_number TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE driver_documents DROP CONSTRAINT IF EXISTS driver_documents_type_check;
ALTER TABLE driver_documents ADD CONSTRAINT driver_documents_type_check CHECK (type IN (
    'driver_license', 'vehicle_license', 'roadworthiness', 'auto_insurance',
    'hackney_permit', 'lasdri', 'lasrra_lag_id',
    'driver_picture', 'selfie_with_id', 'id_document'
));

ALTER TABLE driver_document_uploads DROP CONSTRAINT IF EXISTS driver_document_uploads_type_check;
ALTER TABLE driver_document_uploads ADD CONSTRAINT driver_document_uploads_type_check CHECK (type IN (
    'driver_license', 'vehicle_license', 'roadworthiness', 'auto_insurance',
    'hackney_permit', 'lasdri', 'lasrra_lag_id',
    'driver_picture', 'selfie_with_id', 'id_document'
));
