package simulation

import (
	"errors"
	"github.com/google/uuid"
	"math"
	"strings"
	"time"
)

type Participant struct {
	DriveTrip           bool     `json:"driveTrip"`
	MovementSpeedMPS    float64  `json:"movementSpeedMps"`
	WaitAtPickupSeconds int      `json:"waitAtPickupSeconds"`
	Role                string   `json:"role"`
	Name                string   `json:"name"`
	Controller          string   `json:"controller"`
	Latitude            float64  `json:"latitude"`
	Longitude           float64  `json:"longitude"`
	Approval            string   `json:"approval"`
	AutoAccept          bool     `json:"autoAccept"`
	Online              bool     `json:"online"`
	BalanceKobo         int64    `json:"balanceKobo"`
	ModelID             string   `json:"modelId"`
	ModelYear           int      `json:"modelYear"`
	CategoryIDs         []string `json:"categoryIds"`
}

func (p Participant) Validate() error {
	if p.WaitAtPickupSeconds < 0 || p.WaitAtPickupSeconds > 600 {
		return errors.New("pickup wait must be between 0 and 600 seconds")
	}
	if p.DriveTrip && (math.IsNaN(p.MovementSpeedMPS) || math.IsInf(p.MovementSpeedMPS, 0) || p.MovementSpeedMPS <= 0 || p.MovementSpeedMPS > 100) {
		return errors.New("movement speed must be greater than zero and at most 100 m/s")
	}
	if p.Role != "driver" && p.Role != "rider" {
		return errors.New("role must be driver or rider")
	}
	if strings.TrimSpace(p.Name) == "" || len(p.Name) > 100 {
		return errors.New("participant name required, maximum 100 bytes")
	}
	if p.Controller != "simulator" && p.Controller != "app" {
		return errors.New("controller must be simulator or app")
	}
	if math.IsNaN(p.Latitude) || math.IsInf(p.Latitude, 0) || math.IsNaN(p.Longitude) || math.IsInf(p.Longitude, 0) || math.Abs(p.Latitude) > 90 || math.Abs(p.Longitude) > 180 {
		return errors.New("valid coordinates required")
	}
	if p.Role == "rider" {
		if p.DriveTrip || p.MovementSpeedMPS != 0 || p.WaitAtPickupSeconds != 0 {
			return errors.New("Driver movement cannot be applied to a Rider")
		}
		if p.Approval != "" || p.AutoAccept || p.Online || p.BalanceKobo != 0 || p.ModelID != "" || p.ModelYear != 0 || len(p.CategoryIDs) > 0 {
			return errors.New("Driver conditions cannot be applied to a Rider")
		}
		return nil
	}
	if p.Approval != "approved" && p.Approval != "pending" {
		return errors.New("Driver approval must be approved or pending")
	}
	if p.BalanceKobo < -100000000 || p.BalanceKobo > 1000000000 {
		return errors.New("balance outside fixture bounds")
	}
	if p.ModelYear != 0 && (p.ModelYear < 1900 || p.ModelYear > time.Now().Year()+1) {
		return errors.New("invalid model year")
	}
	if p.ModelID != "" {
		if _, e := uuid.Parse(p.ModelID); e != nil {
			return errors.New("invalid model ID")
		}
		if p.ModelYear == 0 {
			return errors.New("model year required with model ID")
		}
	}
	if len(p.CategoryIDs) > 32 {
		return errors.New("too many category memberships")
	}
	seen := map[string]bool{}
	for _, id := range p.CategoryIDs {
		if _, e := uuid.Parse(id); e != nil || seen[id] {
			return errors.New("invalid or duplicate category ID")
		}
		seen[id] = true
	}
	return nil
}
