-- A stable public reference separate from the internal Driver UUID.
ALTER TABLE driver_profiles ADD COLUMN IF NOT EXISTS application_number TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS driver_profiles_application_number_unique ON driver_profiles(application_number);

CREATE OR REPLACE FUNCTION next_driver_application_number() RETURNS TEXT
LANGUAGE plpgsql VOLATILE AS $$
DECLARE candidate TEXT;
BEGIN
    -- Serialize assignment until the inserting transaction commits, including
    -- the rare collision-retry path and concurrent profile creation.
    PERFORM pg_advisory_xact_lock(700001);
    LOOP
        candidate := 'HGD-' || (floor(random() * 900000000000) + 100000000000)::BIGINT::TEXT;
        IF NOT EXISTS (SELECT 1 FROM driver_profiles WHERE application_number=candidate) THEN
            RETURN candidate;
        END IF;
    END LOOP;
END;
$$;

UPDATE driver_profiles SET application_number=next_driver_application_number() WHERE application_number IS NULL;
ALTER TABLE driver_profiles ALTER COLUMN application_number SET DEFAULT next_driver_application_number();
ALTER TABLE driver_profiles ALTER COLUMN application_number SET NOT NULL;
ALTER TABLE driver_profiles ADD CONSTRAINT driver_application_number_format CHECK (application_number ~ '^HGD-[0-9]{12}$');
