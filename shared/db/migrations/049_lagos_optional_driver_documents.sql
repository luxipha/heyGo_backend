-- Lagos-specific compliance evidence follows the existing Driver document flow.
-- These are optional, market-dependent requirements rather than part of the
-- universal online-eligibility document set.
ALTER TABLE driver_documents
    DROP CONSTRAINT IF EXISTS driver_documents_type_check;
ALTER TABLE driver_documents
    ADD CONSTRAINT driver_documents_type_check CHECK (type IN (
        'driver_license', 'vehicle_license', 'roadworthiness', 'auto_insurance',
        'hackney_permit', 'lasdri', 'lasrra_lag_id'
    ));

ALTER TABLE driver_document_uploads
    DROP CONSTRAINT IF EXISTS driver_document_uploads_type_check;
ALTER TABLE driver_document_uploads
    ADD CONSTRAINT driver_document_uploads_type_check CHECK (type IN (
        'driver_license', 'vehicle_license', 'roadworthiness', 'auto_insurance',
        'hackney_permit', 'lasdri', 'lasrra_lag_id'
    ));
