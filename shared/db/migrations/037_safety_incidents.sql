-- Safety decisions are tracked separately from Support's case conversation.
CREATE TABLE IF NOT EXISTS safety_incidents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    case_id UUID NOT NULL UNIQUE REFERENCES support_cases(id),
    status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','acknowledged','investigating','escalated','resolved')),
    severity TEXT NOT NULL DEFAULT 'untriaged' CHECK (severity IN ('untriaged','low','medium','high','critical')),
    assigned_admin_id UUID REFERENCES admin_users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at TIMESTAMPTZ,
    CHECK ((status='resolved')=(resolved_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS safety_incidents_queue_idx ON safety_incidents(status,severity,created_at DESC,id DESC);

CREATE TABLE IF NOT EXISTS safety_incident_actions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id UUID NOT NULL REFERENCES safety_incidents(id),
    actor_admin_id UUID NOT NULL REFERENCES admin_users(id),
    old_status TEXT NOT NULL,
    new_status TEXT NOT NULL,
    old_severity TEXT NOT NULL,
    new_severity TEXT NOT NULL,
    reason TEXT NOT NULL CHECK (length(trim(reason)) BETWEEN 1 AND 1000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS safety_incident_actions_incident_idx ON safety_incident_actions(incident_id,created_at,id);
CREATE OR REPLACE FUNCTION protect_safety_incident_actions() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'safety incident actions are append only';
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER safety_incident_actions_append_only BEFORE UPDATE OR DELETE ON safety_incident_actions
    FOR EACH ROW EXECUTE FUNCTION protect_safety_incident_actions();

CREATE OR REPLACE FUNCTION create_safety_incident_for_case() RETURNS TRIGGER AS $$
BEGIN
    IF NEW.topic_code IN ('safety','general-safety') THEN
        INSERT INTO safety_incidents(case_id,created_at,updated_at) VALUES(NEW.id,NEW.created_at,NEW.created_at)
        ON CONFLICT(case_id) DO NOTHING;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS support_case_safety_incident ON support_cases;
CREATE TRIGGER support_case_safety_incident AFTER INSERT ON support_cases
    FOR EACH ROW EXECUTE FUNCTION create_safety_incident_for_case();
INSERT INTO safety_incidents(case_id,created_at,updated_at)
SELECT id,created_at,updated_at FROM support_cases WHERE topic_code IN ('safety','general-safety')
ON CONFLICT(case_id) DO NOTHING;
