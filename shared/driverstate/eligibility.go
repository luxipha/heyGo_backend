package driverstate

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

var documentTypes = []string{"driver_license", "vehicle_license", "roadworthiness", "auto_insurance", "hackney_permit"}

type Requirement struct {
	Type     string `json:"type"`
	Status   string `json:"status"`
	Complete bool   `json:"complete"`
}

type Eligibility struct {
	Requirements    []Requirement `json:"requirements"`
	Completed       int           `json:"completed"`
	Required        int           `json:"required"`
	AdminStatus     string        `json:"adminStatus"`
	EligibleToDrive bool          `json:"eligibleToDrive"`
}

func Read(ctx context.Context, pool *pgxpool.Pool, driverID string) (Eligibility, error) {
	result := Eligibility{Required: 7, AdminStatus: "pending"}
	var nin, verified bool
	var inspection string
	err := pool.QueryRow(ctx, `SELECT u.nin_verified,u.verified, COALESCE(p.admin_status,'pending'),
		COALESCE((SELECT i.status FROM driver_inspections i WHERE i.driver_id=u.id ORDER BY i.created_at DESC, i.id DESC LIMIT 1),'required')
		FROM users u LEFT JOIN driver_profiles p ON p.driver_id=u.id WHERE u.id=$1::UUID AND 'driver'=ANY(u.roles)`, driverID).Scan(&nin, &verified, &result.AdminStatus, &inspection)
	if err != nil {
		return Eligibility{}, fmt.Errorf("read driver eligibility: %w", err)
	}
	if nin && verified {
		result.Requirements = append(result.Requirements, Requirement{Type: "nin", Status: "approved", Complete: true})
	} else {
		result.Requirements = append(result.Requirements, Requirement{Type: "nin", Status: "required"})
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
	for _, kind := range documentTypes {
		status := documents[kind]
		if status == "" {
			status = "required"
		}
		result.Requirements = append(result.Requirements, Requirement{Type: kind, Status: status, Complete: status == "approved"})
	}
	result.Requirements = append(result.Requirements, Requirement{Type: "vehicle_inspection", Status: inspection, Complete: inspection == "completed"})
	for _, requirement := range result.Requirements {
		if requirement.Complete {
			result.Completed++
		}
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE((SELECT eligible_to_drive FROM driver_eligibility WHERE driver_id=$1::UUID),FALSE)`, driverID).Scan(&result.EligibleToDrive); err != nil {
		return Eligibility{}, fmt.Errorf("read driver online gate: %w", err)
	}
	return result, nil
}
