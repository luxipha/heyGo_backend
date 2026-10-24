package handler

import "fmt"

// VehicleFindings describes the actual inspected car, not its ride category.
// Empty values explicitly mean not assessed; no pass is inferred.
type VehicleFindings struct {
	EngineCapacityCC *int              `json:"engineCapacityCc"`
	FuelType         string            `json:"fuelType"`
	Transmission     string            `json:"transmission"`
	Doors            *int              `json:"doors"`
	Checks           map[string]string `json:"checks"`
}

var vehicleConditionChecks = []string{"tyres", "brakes", "lights", "seatbelts", "mirrors", "windscreen", "doors"}

func (f VehicleFindings) validate() error {
	if f.EngineCapacityCC != nil && (*f.EngineCapacityCC < 1 || *f.EngineCapacityCC > 20000) {
		return fmt.Errorf("engine capacity must be 1–20000 cc, or unknown")
	}
	if f.Doors != nil && (*f.Doors < 1 || *f.Doors > 10) {
		return fmt.Errorf("doors must be 1–10, or unknown")
	}
	if !oneOf(f.FuelType, "", "not_assessed", "petrol", "diesel", "hybrid", "plug_in_hybrid", "electric", "hydrogen", "cng", "lpg", "other") {
		return fmt.Errorf("invalid fuel type")
	}
	if !oneOf(f.Transmission, "", "not_assessed", "manual", "automatic", "cvt", "single_speed", "other") {
		return fmt.Errorf("invalid transmission")
	}
	if f.FuelType == "electric" && f.EngineCapacityCC != nil {
		return fmt.Errorf("engine capacity does not apply to a battery-electric vehicle")
	}
	for key, status := range f.Checks {
		if !oneOf(key, vehicleConditionChecks...) || !oneOf(status, "not_assessed", "pass", "fail") {
			return fmt.Errorf("invalid vehicle condition check")
		}
	}
	return nil
}
func oneOf(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}
