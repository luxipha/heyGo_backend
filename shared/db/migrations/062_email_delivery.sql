-- Existing Resend installations remain enabled; SMTP requires explicit configuration.
CREATE TABLE admin_email_delivery_settings (
 id INTEGER PRIMARY KEY CHECK (id=1),
 resend_enabled BOOLEAN NOT NULL DEFAULT TRUE,
 smtp_enabled BOOLEAN NOT NULL DEFAULT FALSE,
 primary_provider TEXT NOT NULL DEFAULT 'resend' CHECK(primary_provider IN ('resend','smtp')),
 fallback_enabled BOOLEAN NOT NULL DEFAULT FALSE,
 smtp_host TEXT NOT NULL DEFAULT '',
 smtp_port INTEGER NOT NULL DEFAULT 587 CHECK(smtp_port BETWEEN 1 AND 65535),
 smtp_tls_mode TEXT NOT NULL DEFAULT 'starttls' CHECK(smtp_tls_mode IN ('starttls','tls')),
 smtp_username TEXT NOT NULL DEFAULT '',
 smtp_password_ciphertext BYTEA,
 smtp_from_email TEXT NOT NULL DEFAULT '',
 smtp_from_name TEXT NOT NULL DEFAULT 'HeyGo',
 updated_by UUID REFERENCES admin_users(id),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
