CREATE TABLE admin_monnify_settings (
 version BIGSERIAL PRIMARY KEY, enabled BOOLEAN NOT NULL DEFAULT false,
 trip_payments_enabled BOOLEAN NOT NULL DEFAULT false,
 environment TEXT NOT NULL CHECK(environment IN('sandbox','live')),
 api_key_ciphertext BYTEA, secret_key_ciphertext BYTEA, contract_code_ciphertext BYTEA,
 updated_by UUID REFERENCES admin_users(id),created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 CHECK(NOT trip_payments_enabled OR enabled)
);
CREATE TRIGGER monnify_settings_immutable BEFORE UPDATE OR DELETE ON admin_monnify_settings FOR EACH ROW EXECUTE FUNCTION heroes_immutable_record();
ALTER TABLE driver_operating_topups ADD COLUMN monnify_config_version BIGINT REFERENCES admin_monnify_settings(version);
ALTER TABLE rider_no_show_debt_payments ADD COLUMN monnify_config_version BIGINT REFERENCES admin_monnify_settings(version);
ALTER TABLE payments ADD COLUMN monnify_config_version BIGINT REFERENCES admin_monnify_settings(version);
CREATE FUNCTION bind_monnify_config() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 SELECT version INTO NEW.monnify_config_version FROM admin_monnify_settings ORDER BY version DESC LIMIT 1;
 RETURN NEW; END $$;
CREATE TRIGGER topup_monnify_config BEFORE INSERT ON driver_operating_topups FOR EACH ROW EXECUTE FUNCTION bind_monnify_config();
CREATE TRIGGER debt_monnify_config BEFORE INSERT ON rider_no_show_debt_payments FOR EACH ROW EXECUTE FUNCTION bind_monnify_config();
CREATE TRIGGER payment_monnify_config BEFORE INSERT ON payments FOR EACH ROW EXECUTE FUNCTION bind_monnify_config();
ALTER TABLE trip_settlements ADD COLUMN collection_method TEXT NOT NULL DEFAULT 'direct' CHECK(collection_method IN('direct','monnify'));
CREATE TABLE rider_trip_payments (
 trip_id UUID PRIMARY KEY REFERENCES trip_settlements(trip_id),rider_id UUID NOT NULL REFERENCES users(id),driver_id UUID NOT NULL REFERENCES users(id),
 amount_kobo BIGINT NOT NULL CHECK(amount_kobo>0), fare_kobo BIGINT NOT NULL CHECK(fare_kobo>=0),debt_kobo BIGINT NOT NULL CHECK(debt_kobo>=0),
 payment_reference TEXT NOT NULL UNIQUE, transaction_reference TEXT UNIQUE,checkout_url TEXT,
 monnify_config_version BIGINT NOT NULL REFERENCES admin_monnify_settings(version),
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN('pending','success','failed','cancelled')),
 initialization_token UUID,initialization_lease_until TIMESTAMPTZ,verified_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),CHECK(amount_kobo=fare_kobo+debt_kobo)
);
CREATE INDEX trip_payment_pending ON rider_trip_payments(updated_at) WHERE status='pending';
ALTER TABLE driver_operating_entries DROP CONSTRAINT driver_operating_entries_kind_check;
ALTER TABLE driver_operating_entries DROP CONSTRAINT driver_operating_entries_check;
ALTER TABLE driver_operating_entries ADD CHECK(kind IN('topup','statutory_charge','reversal','no_show_transfer','rider_credit_payment','referral_reward','trip_collection'));
ALTER TABLE driver_operating_entries ADD CHECK((kind IN('topup','referral_reward','rider_credit_payment','trip_collection') AND delta_kobo>0) OR (kind='statutory_charge' AND delta_kobo<0) OR kind IN('reversal','no_show_transfer'));
