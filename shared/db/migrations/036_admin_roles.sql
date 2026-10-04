-- Existing staff receive no role automatically. Assign each role deliberately before
-- enabling the new permission gate; unassigned sessions fail closed.
ALTER TABLE admin_users ADD COLUMN IF NOT EXISTS role TEXT;
ALTER TABLE admin_users DROP CONSTRAINT IF EXISTS admin_users_role_check;
ALTER TABLE admin_users ADD CONSTRAINT admin_users_role_check CHECK (
    role IS NULL OR role IN ('operations', 'compliance', 'support', 'finance', 'safety', 'super_admin')
);
CREATE INDEX IF NOT EXISTS admin_users_role_idx ON admin_users(role) WHERE active=TRUE;
