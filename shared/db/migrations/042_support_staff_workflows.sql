-- Staff-created cases retain the actual staff actor; opened_by remains the
-- existing customer-facing case owner and is never an impersonated author.
ALTER TABLE support_cases ADD COLUMN IF NOT EXISTS created_by_admin UUID REFERENCES admin_users(id);

INSERT INTO support_topics(code,topic_kind,audience,label,details,sort_order) VALUES
    ('lost_item','trip_issue','both','Lost item','[]'::JSONB,55),
    ('complaint','support','both','Complaint','[]'::JSONB,65)
ON CONFLICT(code) DO NOTHING;

CREATE TABLE IF NOT EXISTS support_case_escalations (
    id UUID PRIMARY KEY,
    case_id UUID NOT NULL REFERENCES support_cases(id),
    actor_admin_id UUID NOT NULL REFERENCES admin_users(id),
    reason TEXT NOT NULL CHECK (char_length(trim(reason)) BETWEEN 5 AND 1000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS support_case_escalations_case_idx ON support_case_escalations(case_id,created_at DESC,id DESC);
CREATE TRIGGER support_case_escalations_append_only BEFORE UPDATE OR DELETE ON support_case_escalations
    FOR EACH ROW EXECUTE FUNCTION protect_financial_ledger();

CREATE TABLE IF NOT EXISTS support_case_outcomes (
    id UUID PRIMARY KEY,
    case_id UUID NOT NULL UNIQUE REFERENCES support_cases(id),
    actor_admin_id UUID NOT NULL REFERENCES admin_users(id),
    outcome TEXT NOT NULL CHECK (outcome IN ('resolved_no_money','referred_to_finance','item_returned','unable_to_resolve','withdrawn')),
    reason TEXT NOT NULL CHECK (char_length(trim(reason)) BETWEEN 5 AND 1000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS support_case_outcomes_finance_idx ON support_case_outcomes(created_at DESC,id DESC)
    WHERE outcome='referred_to_finance';
CREATE TRIGGER support_case_outcomes_append_only BEFORE UPDATE OR DELETE ON support_case_outcomes
    FOR EACH ROW EXECUTE FUNCTION protect_financial_ledger();
