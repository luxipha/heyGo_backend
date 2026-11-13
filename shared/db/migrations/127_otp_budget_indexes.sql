-- Cross-flow destination and authenticated-owner OTP send accounting.
CREATE INDEX auth_otp_challenges_owner_budget ON auth_otp_challenges(pin_reset_user_id,channel,created_at) WHERE pin_reset_user_id IS NOT NULL;
CREATE INDEX rider_contact_challenges_new_budget ON rider_contact_challenges(new_destination,created_at);
CREATE INDEX rider_contact_challenges_current_budget ON rider_contact_challenges(current_destination,created_at) WHERE purpose='phone_change';
