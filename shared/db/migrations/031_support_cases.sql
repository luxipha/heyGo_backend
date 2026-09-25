CREATE TABLE IF NOT EXISTS support_topics (
    code TEXT PRIMARY KEY,
    topic_kind TEXT NOT NULL CHECK (topic_kind IN ('trip_issue','support')),
    audience TEXT NOT NULL CHECK (audience IN ('driver','rider','both')),
    label TEXT NOT NULL CHECK (length(label) BETWEEN 1 AND 120),
    details JSONB NOT NULL DEFAULT '[]'::JSONB CHECK (jsonb_typeof(details)='array'),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    sort_order INTEGER NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO support_topics(code,topic_kind,audience,label,details,sort_order) VALUES
    ('safety','trip_issue','both','Safety concern','[]',10),
    ('fare','trip_issue','both','Fare or payment issue','["Proof of transfer missing","Payment delay","Rider did not pay toll"]',20),
    ('location','trip_issue','both','Pickup or drop-off location','[]',30),
    ('vehicle','trip_issue','both','Vehicle trouble','[]',40),
    ('route','trip_issue','both','Traffic, toll, or route obstruction','["Wrong destination requested"]',50),
    ('other','trip_issue','both','Other trip issue','[]',60),
    ('account','support','both','Account and verification','[]',10),
    ('documents','support','driver','Documents and inspection','[]',20),
    ('payment','support','both','Payments and balance','[]',30),
    ('trip','support','both','Trip support','[]',40),
    ('general-safety','support','both','Safety support','[]',50),
    ('general-other','support','both','Other support','[]',60)
ON CONFLICT(code) DO NOTHING;

CREATE TABLE IF NOT EXISTS support_uploads (
    id UUID PRIMARY KEY,
    owner_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    storage_key TEXT NOT NULL UNIQUE,
    content_type TEXT NOT NULL CHECK (content_type IN ('image/jpeg','image/png','application/pdf')),
    size_bytes BIGINT NOT NULL CHECK (size_bytes BETWEEN 1 AND 10485760),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS support_cases (
    id UUID PRIMARY KEY,
    case_number BIGINT GENERATED ALWAYS AS IDENTITY UNIQUE,
    opened_by UUID NOT NULL REFERENCES users(id),
    audience TEXT NOT NULL CHECK (audience IN ('driver','rider')),
    topic_code TEXT NOT NULL REFERENCES support_topics(code),
    trip_id UUID REFERENCES trips(id),
    detail_code TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL CHECK (length(subject) BETWEEN 1 AND 160),
    status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','in_progress','waiting_on_user','resolved','closed')),
    priority TEXT NOT NULL DEFAULT 'normal' CHECK (priority IN ('normal','high')),
    assigned_admin_id UUID REFERENCES admin_users(id),
    last_message_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS support_cases_queue_idx ON support_cases(status,priority DESC,last_message_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS support_cases_owner_idx ON support_cases(opened_by,last_message_at DESC,id DESC);
CREATE UNIQUE INDEX IF NOT EXISTS support_cases_one_trip_issue_per_topic_idx
    ON support_cases(opened_by,trip_id,topic_code) WHERE trip_id IS NOT NULL AND status NOT IN ('resolved','closed');

CREATE TABLE IF NOT EXISTS support_case_messages (
    id UUID PRIMARY KEY,
    case_id UUID NOT NULL REFERENCES support_cases(id) ON DELETE CASCADE,
    sender_kind TEXT NOT NULL CHECK (sender_kind IN ('user','staff')),
    sender_user_id UUID REFERENCES users(id),
    sender_admin_id UUID REFERENCES admin_users(id),
    client_message_id UUID NOT NULL,
    body TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 4000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((sender_kind='user' AND sender_user_id IS NOT NULL AND sender_admin_id IS NULL) OR
           (sender_kind='staff' AND sender_user_id IS NULL AND sender_admin_id IS NOT NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS support_case_messages_user_idempotency_idx
    ON support_case_messages(case_id,sender_user_id,client_message_id) WHERE sender_kind='user';
CREATE UNIQUE INDEX IF NOT EXISTS support_case_messages_staff_idempotency_idx
    ON support_case_messages(case_id,sender_admin_id,client_message_id) WHERE sender_kind='staff';

CREATE INDEX IF NOT EXISTS support_case_messages_history_idx ON support_case_messages(case_id,created_at DESC,id DESC);

CREATE TABLE IF NOT EXISTS support_case_attachments (
    id UUID PRIMARY KEY,
    message_id UUID NOT NULL REFERENCES support_case_messages(id) ON DELETE CASCADE,
    storage_key TEXT NOT NULL UNIQUE,
    content_type TEXT NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes BETWEEN 1 AND 10485760),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS support_case_reads (
    case_id UUID NOT NULL REFERENCES support_cases(id) ON DELETE CASCADE,
    reader_kind TEXT NOT NULL CHECK (reader_kind IN ('user','staff')),
    reader_id UUID NOT NULL,
    read_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY(case_id,reader_kind,reader_id)
);
