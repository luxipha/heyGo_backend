CREATE TABLE admin_review_otp (
 id INTEGER PRIMARY KEY CHECK(id=1),
 enabled BOOLEAN NOT NULL DEFAULT FALSE,
 email TEXT NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL,
 version BIGINT NOT NULL DEFAULT 1,
 code_ciphertext BYTEA,
 updated_by UUID REFERENCES admin_users(id),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE auth_otp_challenges ADD COLUMN review_otp_version BIGINT;
ALTER TABLE user_sessions ADD COLUMN review_otp_version BIGINT;
