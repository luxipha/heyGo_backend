-- Append-only Compliance evidence used for refund destination ownership checks.
-- The name is encrypted by the API and never copied into broad Driver reads.
CREATE TABLE IF NOT EXISTS driver_verified_legal_names (
    id UUID PRIMARY KEY,
    driver_id UUID NOT NULL REFERENCES driver_profiles(driver_id),
    evidence_document_id UUID NOT NULL REFERENCES driver_documents(id),
    name_ciphertext BYTEA NOT NULL,
    reviewed_by UUID NOT NULL REFERENCES admin_users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS driver_verified_legal_names_current_idx
    ON driver_verified_legal_names(driver_id,created_at DESC,id DESC);
CREATE TRIGGER driver_verified_legal_names_append_only BEFORE UPDATE OR DELETE ON driver_verified_legal_names
    FOR EACH ROW EXECUTE FUNCTION protect_financial_ledger();
