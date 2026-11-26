package handler

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// driverTripPlace is the text a Driver sees for a pickup or destination. Only
// the name and address are exposed: the stored metadata also carries provider
// ids and exact coordinates, which History and receipts do not need.
type driverTripPlace struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// driverTripPlaceFromMetadata reads a ride_fares pickup/destination metadata
// document. It returns nil when nothing readable is stored, so clients can show
// their own fallback instead of an empty string.
func driverTripPlaceFromMetadata(raw []byte) *driverTripPlace {
	if len(raw) == 0 {
		return nil
	}
	var stored struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	}
	if json.Unmarshal(raw, &stored) != nil {
		return nil
	}
	if stored.Name == "" && stored.Address == "" {
		return nil
	}
	return &driverTripPlace{Name: stored.Name, Address: stored.Address}
}

// driverStatutoryCharge is one assessed statutory charge on a completed trip,
// taken from the rule snapshot that was approved when the trip completed.
type driverStatutoryCharge struct {
	LedgerCode    string    `json:"ledgerCode"`
	Name          string    `json:"name"`
	Authority     string    `json:"authority"`
	AmountKobo    int64     `json:"amountKobo"`
	Bearer        string    `json:"bearer"`
	FundingSource string    `json:"fundingSource"`
	Reference     string    `json:"reference,omitempty"`
	AssessedAt    time.Time `json:"assessedAt"`
}

type driverQueryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// readDriverStatutoryCharges returns the Driver's own charges for a trip.
// It always returns a non-nil slice so the JSON field is `[]`, not `null`.
func readDriverStatutoryCharges(ctx context.Context, db driverQueryer, driver, tripID string) ([]driverStatutoryCharge, error) {
	rows, err := db.Query(ctx, `SELECT COALESCE(c.rule_snapshot->>'ledgerCode',r.ledger_code),
		COALESCE(c.rule_snapshot->>'name',r.name),COALESCE(c.rule_snapshot->>'authority',r.authority),
		c.amount_kobo,c.bearer,c.funding_source,COALESCE(c.rule_snapshot->>'reference',''),c.assessed_at
		FROM trip_statutory_charges c JOIN statutory_charge_rules r ON r.id=c.rule_id
		WHERE c.trip_id=$1::UUID AND c.driver_id=$2::UUID ORDER BY c.assessed_at,c.id`, tripID, driver)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	charges := []driverStatutoryCharge{}
	for rows.Next() {
		var charge driverStatutoryCharge
		if err := rows.Scan(&charge.LedgerCode, &charge.Name, &charge.Authority, &charge.AmountKobo, &charge.Bearer, &charge.FundingSource, &charge.Reference, &charge.AssessedAt); err != nil {
			return nil, err
		}
		charges = append(charges, charge)
	}
	return charges, rows.Err()
}

// driverTripRider is what the assigned Driver may know about the Rider: the
// display name the Rider chose and how other Drivers have rated them. There is
// no photo because the platform does not store Rider photos.
type driverTripRider struct {
	DisplayName string   `json:"displayName"`
	Rating      *float64 `json:"rating"`
	RatingCount int64    `json:"ratingCount"`
}

// readDriverTripRider returns nil when the trip is not this Driver's, so the
// name and rating are never readable for someone else's trip.
func readDriverTripRider(ctx context.Context, db interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, driver, tripID string) (*driverTripRider, error) {
	var rider driverTripRider
	err := db.QueryRow(ctx, `SELECT COALESCE(p.display_name,''),
		(SELECT ROUND(AVG(r.rating)::NUMERIC,1)::DOUBLE PRECISION FROM trip_ratings r JOIN trips rt ON rt.id=r.trip_id
			WHERE rt.rider_id=t.rider_id AND r.subject_id=t.rider_id AND r.actor_id=rt.assigned_driver_id),
		(SELECT COUNT(*)::BIGINT FROM trip_ratings r JOIN trips rt ON rt.id=r.trip_id
			WHERE rt.rider_id=t.rider_id AND r.subject_id=t.rider_id AND r.actor_id=rt.assigned_driver_id)
		FROM trips t LEFT JOIN rider_profiles p ON p.rider_id=t.rider_id
		WHERE t.id=$1::UUID AND t.assigned_driver_id=$2::UUID`, tripID, driver).
		Scan(&rider.DisplayName, &rider.Rating, &rider.RatingCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rider, nil
}
