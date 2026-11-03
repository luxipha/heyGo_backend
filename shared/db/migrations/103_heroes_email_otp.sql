ALTER TABLE heroes_accounts ALTER COLUMN password_hash DROP NOT NULL;
ALTER TABLE heroes_accounts DROP CONSTRAINT heroes_accounts_name_check;
ALTER TABLE heroes_accounts ADD CONSTRAINT heroes_accounts_name_check CHECK(length(name) BETWEEN 0 AND 100);
ALTER TABLE heroes_accounts ADD COLUMN profile_completed_at TIMESTAMPTZ;
UPDATE heroes_accounts SET profile_completed_at=created_at WHERE length(trim(name))>0;
CREATE TABLE heroes_email_otps (
 email TEXT PRIMARY KEY, challenge_id UUID NOT NULL UNIQUE, code_hash TEXT NOT NULL,
 requested_at TIMESTAMPTZ NOT NULL DEFAULT now(), expires_at TIMESTAMPTZ NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0, used_at TIMESTAMPTZ
);
