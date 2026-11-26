package driverstate

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// documentTypes are the documents every driver submits, in checklist order.
// The driver's licence is always required; the rest follow the configuration
// in driver_requirement_config (see migration 139).
var documentTypes = []string{"driver_license", "vehicle_license", "roadworthiness", "auto_insurance", "hackney_permit"}

const (
	ModeRequired = "required"
	ModeOptional = "optional"
	ModeOff      = "off"
)

type Requirement struct {
	Type     string `json:"type"`
	Status   string `json:"status"`
	Complete bool   `json:"complete"`
	// Mode is "required" or "optional". Requirements that are off are not listed.
	Mode string `json:"mode"`
	// GraceUntil is set while a newly required item does not yet block this
	// driver (they registered before it became required).
	GraceUntil *time.Time `json:"graceUntil,omitempty"`
}

// Config is the admin-set mode of one configurable requirement.
type Config struct {
	Mode          string
	GraceDays     int
	RequiredSince *time.Time
}

// Enforced reports whether the requirement blocks a driver who registered at
// registeredAt, and when their grace window ends if they are inside one.
func (c Config) Enforced(registeredAt, now time.Time) (enforced bool, graceUntil *time.Time) {
	if c.Mode != ModeRequired {
		return false, nil
	}
	if c.RequiredSince != nil && registeredAt.Before(*c.RequiredSince) {
		until := c.RequiredSince.AddDate(0, 0, c.GraceDays)
		if now.Before(until) {
			return false, &until
		}
	}
	return true, nil
}

// DefaultConfig is used if a row is missing: the original all-required rule.
func DefaultConfig() Config { return Config{Mode: ModeRequired, GraceDays: 14} }

type Eligibility struct {
	Requirements    []Requirement `json:"requirements"`
	Completed       int           `json:"completed"`
	Required        int           `json:"required"`
	AdminStatus     string        `json:"adminStatus"`
	EligibleToDrive bool          `json:"eligibleToDrive"`
}

func Read(ctx context.Context, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, driverID string) (Eligibility, error) {
	result := Eligibility{AdminStatus: "pending"}
	var accountVerified bool
	var manualVerification string
	var inspection string
	err := pool.QueryRow(ctx, `SELECT u.phone_verified_at IS NOT NULL OR u.email_verified,CASE WHEN COALESCE(p.manual_verification_status,'pending')='pending' AND NOT EXISTS(SELECT 1 FROM driver_identity_profiles ip WHERE ip.driver_id=u.id) THEN 'required' ELSE COALESCE(p.manual_verification_status,'required') END,COALESCE(p.admin_status,'pending'),
		COALESCE((SELECT CASE WHEN i.vehicle_revision=COALESCE((SELECT v.inspection_revision FROM driver_vehicles v WHERE v.driver_id=u.id),0) THEN i.status ELSE 'required' END FROM driver_inspections i WHERE i.driver_id=u.id ORDER BY i.created_at DESC, i.id DESC LIMIT 1),'required')
		FROM users u LEFT JOIN driver_profiles p ON p.driver_id=u.id WHERE u.id=$1::UUID AND 'driver'=ANY(u.roles)`, driverID).Scan(&accountVerified, &manualVerification, &result.AdminStatus, &inspection)
	if err != nil {
		return Eligibility{}, fmt.Errorf("read driver eligibility: %w", err)
	}
	if accountVerified && manualVerification == "approved" {
		result.Requirements = append(result.Requirements, Requirement{Type: "manual_identity", Status: "approved", Complete: true, Mode: ModeRequired})
	} else {
		status := manualVerification
		if !accountVerified || status == "" {
			status = "required"
		}
		result.Requirements = append(result.Requirements, Requirement{Type: "manual_identity", Status: status, Mode: ModeRequired})
	}
	rows, err := pool.Query(ctx, `SELECT DISTINCT ON (type) type,
		CASE WHEN status='approved' AND expires_at IS NOT NULL AND expires_at<=NOW() THEN 'expired' ELSE status END
		FROM driver_documents
		WHERE driver_id=$1::UUID ORDER BY type,submitted_at DESC,id DESC`, driverID)
	if err != nil {
		return Eligibility{}, fmt.Errorf("read driver documents: %w", err)
	}
	documents := make(map[string]string)
	for rows.Next() {
		var kind, status string
		if err := rows.Scan(&kind, &status); err != nil {
			rows.Close()
			return Eligibility{}, err
		}
		documents[kind] = status
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Eligibility{}, err
	}
	rows.Close()
	configs, registeredAt, err := readConfig(ctx, pool, driverID)
	if err != nil {
		return Eligibility{}, err
	}
	now := time.Now()
	add := func(kind, status string, complete bool) {
		cfg, configurable := configs[kind]
		if !configurable {
			cfg = Config{Mode: ModeRequired}
		}
		if cfg.Mode == ModeOff {
			return
		}
		requirement := Requirement{Type: kind, Status: status, Complete: complete, Mode: cfg.Mode}
		if cfg.Mode == ModeRequired {
			// Inside the grace window the item is still listed as required, but
			// it does not block going online yet.
			_, requirement.GraceUntil = cfg.Enforced(registeredAt, now)
			result.Required++
		}
		result.Requirements = append(result.Requirements, requirement)
	}
	// Identity is always required.
	result.Required++
	for _, kind := range documentTypes {
		status := documents[kind]
		if status == "" {
			status = "required"
		}
		add(kind, status, status == "approved")
	}
	add("vehicle_inspection", inspection, inspection == "completed")
	for _, requirement := range result.Requirements {
		if requirement.Complete && requirement.Mode == ModeRequired {
			result.Completed++
		}
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE((SELECT eligible_to_drive FROM driver_eligibility WHERE driver_id=$1::UUID),FALSE)`, driverID).Scan(&result.EligibleToDrive); err != nil {
		return Eligibility{}, fmt.Errorf("read driver online gate: %w", err)
	}
	return result, nil
}

// readConfig loads the configurable requirements and when the driver registered.
func readConfig(ctx context.Context, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, driverID string) (map[string]Config, time.Time, error) {
	var registeredAt time.Time
	if err := pool.QueryRow(ctx, `SELECT created_at FROM users WHERE id=$1::UUID`, driverID).Scan(&registeredAt); err != nil {
		return nil, time.Time{}, fmt.Errorf("read driver registration: %w", err)
	}
	rows, err := pool.Query(ctx, `SELECT type,mode,grace_days,required_since FROM driver_requirement_config`)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("read requirement config: %w", err)
	}
	defer rows.Close()
	configs := map[string]Config{}
	for rows.Next() {
		var kind string
		var cfg Config
		if err := rows.Scan(&kind, &cfg.Mode, &cfg.GraceDays, &cfg.RequiredSince); err != nil {
			return nil, time.Time{}, err
		}
		configs[kind] = cfg
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return configs, registeredAt, nil
}
