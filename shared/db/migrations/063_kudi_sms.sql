CREATE TABLE admin_sms_settings (
 id INTEGER PRIMARY KEY CHECK(id=1),
 enabled BOOLEAN NOT NULL DEFAULT FALSE,
 api_key_ciphertext BYTEA,
 sender_id TEXT NOT NULL DEFAULT '',
 app_name_code TEXT NOT NULL DEFAULT '',
 template_code TEXT NOT NULL DEFAULT '',
 updated_by UUID REFERENCES admin_users(id),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
