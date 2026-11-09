-- VIN / chassis number recorded by staff during vehicle inspection.
-- Stored normalised (uppercase letters and digits only). Empty means not yet
-- recorded; older vehicles keep '' until their next inspection save.
ALTER TABLE driver_vehicle_records ADD COLUMN vin TEXT NOT NULL DEFAULT ''
  CHECK (vin = '' OR vin ~ '^[A-Z0-9]{5,30}$');

-- driver_vehicles is a SELECT * view; re-create it so the new column is exposed.
-- The new column is appended last, so OR REPLACE keeps existing column order.
CREATE OR REPLACE VIEW driver_vehicles AS
  SELECT * FROM driver_vehicle_records WHERE id = selected_driver_vehicle(driver_id);
