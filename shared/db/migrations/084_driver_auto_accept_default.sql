-- Default new preference records to auto-accept; retain existing driver choices.
ALTER TABLE driver_ride_preferences ALTER COLUMN auto_accept SET DEFAULT TRUE;
