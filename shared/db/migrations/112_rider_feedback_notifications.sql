-- Feedback messages are read-only Admin broadcasts; trip ratings are unchanged.
ALTER TABLE rider_notifications DROP CONSTRAINT rider_notifications_type_check;
ALTER TABLE rider_notifications ADD CONSTRAINT rider_notifications_type_check CHECK(type IN ('ride_updates','account_alerts','promotions','feedback'));
ALTER TABLE admin_push_broadcasts DROP CONSTRAINT admin_push_broadcasts_purpose_check;
ALTER TABLE admin_push_broadcasts ADD CONSTRAINT admin_push_broadcasts_purpose_check CHECK(purpose IN ('announcement','marketing','feedback'));
-- Feedback push shares the existing optional promotions preference.
-- Inbox messages remain retained regardless of push preferences.
