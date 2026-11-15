-- Read-only caller workspace. No driver assignments or activity records.
ALTER TABLE admin_users DROP CONSTRAINT admin_users_role_check;
ALTER TABLE admin_users ADD CONSTRAINT admin_users_role_check CHECK (
    role IS NULL OR role IN ('operations','compliance','support','finance','safety','super_admin','onboarding')
);
ALTER TABLE admin_user_roles DROP CONSTRAINT admin_user_roles_role_check;
ALTER TABLE admin_user_roles ADD CONSTRAINT admin_user_roles_role_check CHECK (
    role IN ('operations','compliance','support','finance','safety','super_admin','onboarding')
);
