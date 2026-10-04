-- Keep admin_users.role as the primary role for older clients and staff tools.
-- Additional grants are explicit and are changed together with that column.
CREATE TABLE IF NOT EXISTS admin_user_roles (
    admin_id UUID NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('operations', 'compliance', 'support', 'finance', 'safety', 'super_admin')),
    granted_by UUID REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (admin_id, role)
);
CREATE INDEX IF NOT EXISTS admin_user_roles_role_idx ON admin_user_roles(role);
INSERT INTO admin_user_roles(admin_id,role)
SELECT id,role FROM admin_users WHERE role IS NOT NULL
ON CONFLICT (admin_id,role) DO NOTHING;
