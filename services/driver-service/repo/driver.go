package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/observe/correlation"
	pb "github.com/luxipha/heyGo_backend/shared/proto/driver"
)

var ErrNoAvailableDriver = errors.New("no available driver")
var ErrTripAlreadyMatched = errors.New("trip is already matched")

type Candidate struct {
	Driver    *pb.Driver
	Distance  float64
	Attempt   int
	ExpiresAt time.Time
}
type RetryTrip struct {
	TripID           string
	Payload          []byte
	PreviousDriverID string
}

type DriverRepo interface {
	UpsertOnline(context.Context, *pb.Driver) (*pb.Driver, error)
	SetOffline(context.Context, string) error
	UpdateLocation(context.Context, string, float64, float64) (string, error)
	MatchAndReserve(context.Context, string, string, float64, float64, float64, time.Duration, []byte) (*Candidate, error)
	DeclineAssignment(context.Context, string, string) (*RetryTrip, error)
	ExpireOffers(context.Context, int) ([]RetryTrip, error)
}

type postgresDriverRepo struct{ pool *pgxpool.Pool }

func NewPostgresDriverRepository(pool *pgxpool.Pool) DriverRepo {
	return &postgresDriverRepo{pool: pool}
}

func (r *postgresDriverRepo) UpsertOnline(ctx context.Context, d *pb.Driver) (*pb.Driver, error) {
	result, err := r.pool.Exec(ctx, `INSERT INTO drivers(id,name,profile_pic,car_plate,package_slug,status,available,online_requested,location,online_market_code,last_seen_at)
	SELECT p.driver_id,p.display_name,p.photo_url,v.plate,v.package_slug,'available',TRUE,TRUE,l.location,driver_market_at(l.location),NOW()
	FROM driver_profiles p JOIN driver_vehicles v ON v.driver_id=p.driver_id
	JOIN driver_eligibility e ON e.driver_id=p.driver_id
	JOIN driver_live_locations l ON l.driver_id=p.driver_id
	WHERE p.driver_id=$1::UUID AND v.package_slug=$2 AND e.eligible_to_drive=TRUE
	AND l.recorded_at>=NOW()-INTERVAL '30 minutes'
	AND driver_balance_eligible(p.driver_id,driver_market_at(l.location))
	ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name,profile_pic=EXCLUDED.profile_pic,car_plate=EXCLUDED.car_plate,package_slug=EXCLUDED.package_slug,
	status=CASE WHEN drivers.status IN ('on_trip','offered') THEN drivers.status ELSE 'available' END,
	available=CASE WHEN drivers.status IN ('on_trip','offered') THEN FALSE ELSE TRUE END,online_requested=TRUE,
	location=EXCLUDED.location,online_market_code=EXCLUDED.online_market_code,last_seen_at=NOW(),updated_at=NOW()`, d.Id, d.PackageSlug)
	if err != nil {
		return nil, fmt.Errorf("register driver: %w", err)
	}
	if result.RowsAffected() == 0 {
		return nil, fmt.Errorf("driver is not eligible to go online or package does not match")
	}
	return r.get(ctx, d.Id)
}

func (r *postgresDriverRepo) SetOffline(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `UPDATE drivers SET status=CASE WHEN status='on_trip' THEN status ELSE 'offline' END,available=FALSE,online_requested=FALSE,last_seen_at=NOW(),updated_at=NOW() WHERE id=$1::UUID`, id)
	if err != nil {
		return fmt.Errorf("set driver offline: %w", err)
	}
	return nil
}

func (r *postgresDriverRepo) UpdateLocation(ctx context.Context, id string, lat, lng float64) (string, error) {
	if lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return "", fmt.Errorf("invalid coordinates")
	}
	// The gateway persists the reading before publishing the command. Read
	// that row so a delayed Pub/Sub message cannot move a driver backwards.
	_, err := r.pool.Exec(ctx, `SELECT refresh_driver_operating_market($1::UUID)`, id)
	if err != nil {
		return "", fmt.Errorf("update online driver market: %w", err)
	}
	var riderID string
	err = r.pool.QueryRow(ctx, `SELECT rider_id::TEXT FROM trips WHERE assigned_driver_id=$1::UUID AND status IN ('accepted','started') ORDER BY updated_at DESC LIMIT 1`, id).Scan(&riderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("find assigned rider: %w", err)
	}
	return riderID, nil
}

func (r *postgresDriverRepo) MatchAndReserve(ctx context.Context, tripID, packageSlug string, lat, lng, radius float64, ttl time.Duration, payload []byte) (*Candidate, error) {
	if !json.Valid(payload) {
		return nil, fmt.Errorf("invalid trip payload")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var tripStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM trips WHERE id=$1::UUID FOR UPDATE`, tripID).Scan(&tripStatus); err != nil {
		return nil, fmt.Errorf("lock trip for matching: %w", err)
	}
	if tripStatus != "pending" {
		return nil, ErrTripAlreadyMatched
	}
	c := &Candidate{Driver: &pb.Driver{Location: &pb.Location{}}}
	err = tx.QueryRow(ctx, `SELECT d.id::TEXT,d.name,d.profile_pic,d.car_plate,d.package_slug,ST_Y(d.location::geometry),ST_X(d.location::geometry),ST_Distance(d.location,ST_SetSRID(ST_MakePoint($3,$2),4326)::geography)
	FROM drivers d JOIN users u ON u.id=d.id JOIN driver_eligibility e ON e.driver_id=d.id
	JOIN driver_live_locations l ON l.driver_id=d.id JOIN trips t ON t.id=$5::UUID
	WHERE d.package_slug=$1 AND d.status='available' AND d.available=TRUE AND d.online_requested=TRUE AND d.location IS NOT NULL AND e.eligible_to_drive=TRUE
	AND EXISTS (SELECT 1 FROM driver_socket_sessions s WHERE s.driver_id=d.id AND s.expires_at>NOW())
	AND d.online_market_code=t.market_code AND driver_market_at(d.location)=t.market_code
	AND l.recorded_at>=NOW()-INTERVAL '30 minutes'
	AND driver_balance_eligible(d.id,d.online_market_code)
	AND ST_DWithin(d.location,ST_SetSRID(ST_MakePoint($3,$2),4326)::geography,$4) AND NOT EXISTS(SELECT 1 FROM driver_assignments a WHERE a.trip_id=$5::UUID AND a.driver_id=d.id)
	ORDER BY ST_Distance(d.location,ST_SetSRID(ST_MakePoint($3,$2),4326)::geography)-COALESCE(u.trust_score,100)*2,d.last_seen_at DESC FOR UPDATE OF d SKIP LOCKED LIMIT 1`, packageSlug, lat, lng, radius, tripID).Scan(&c.Driver.Id, &c.Driver.Name, &c.Driver.ProfilePic, &c.Driver.CarPlate, &c.Driver.PackageSlug, &c.Driver.Location.Latitude, &c.Driver.Location.Longitude, &c.Distance)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoAvailableDriver
	}
	if err != nil {
		return nil, fmt.Errorf("find nearby driver: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(attempt),0)+1 FROM driver_assignments WHERE trip_id=$1::UUID`, tripID).Scan(&c.Attempt); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `INSERT INTO driver_assignments(trip_id,driver_id,attempt,status,expires_at,trip_payload) VALUES($1::UUID,$2::UUID,$3,'offered',NOW()+make_interval(secs=>$4),$5) RETURNING expires_at`, tripID, c.Driver.Id, c.Attempt, ttl.Seconds(), payload).Scan(&c.ExpiresAt); err != nil {
		return nil, fmt.Errorf("create driver offer: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE drivers SET status='offered',available=FALSE,updated_at=NOW() WHERE id=$1::UUID`, c.Driver.Id); err != nil {
		return nil, err
	}
	result, err := tx.Exec(ctx, `UPDATE trips SET status='assigned',assigned_driver_id=$2::UUID,updated_at=NOW(),version=version+1 WHERE id=$1::UUID AND status IN('pending','assigned')`, tripID, c.Driver.Id)
	if err != nil {
		return nil, err
	}
	if result.RowsAffected() != 1 {
		return nil, fmt.Errorf("trip is no longer matchable")
	}
	var tripEvent messaging.TripEventData
	if err := json.Unmarshal(payload, &tripEvent); err != nil {
		return nil, fmt.Errorf("invalid trip offer source payload: %w", err)
	}
	if tripEvent.Trip == nil {
		return nil, fmt.Errorf("trip offer source payload has no trip")
	}
	tripEvent.Trip.Status = "assigned"
	tripJSON, err := json.Marshal(tripEvent.Trip)
	if err != nil {
		return nil, err
	}
	tripJSON, err = mergeNoShowDebtOfferFields(payload, tripJSON)
	if err != nil {
		return nil, err
	}
	offer, err := json.Marshal(map[string]any{"trip": json.RawMessage(tripJSON), "offer": map[string]any{"attempt": c.Attempt, "expiresAt": c.ExpiresAt}})
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO trip_event_outbox(trip_id,topic,recipient_id,payload,correlation_id)
		VALUES($1::UUID,$2,$3::UUID,$4::JSONB,$5) ON CONFLICT DO NOTHING`,
		tripID, contracts.DriverCmdTripRequest, c.Driver.Id, offer, correlation.FromContext(ctx)); err != nil {
		return nil, fmt.Errorf("enqueue driver offer: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func mergeNoShowDebtOfferFields(source, tripJSON []byte) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(source, &envelope); err != nil {
		return nil, err
	}
	rawTrip, ok := envelope["trip"]
	if !ok {
		return tripJSON, nil
	}
	var sourceTrip map[string]json.RawMessage
	if err := json.Unmarshal(rawTrip, &sourceTrip); err != nil {
		return nil, err
	}
	debt, hasDebt := sourceTrip["noShowDebtKobo"]
	due, hasDue := sourceTrip["amountDueKobo"]
	if !hasDebt && !hasDue {
		return tripJSON, nil
	}
	var destination map[string]json.RawMessage
	if err := json.Unmarshal(tripJSON, &destination); err != nil {
		return nil, err
	}
	var selected map[string]json.RawMessage
	if err := json.Unmarshal(destination["selectedFare"], &selected); err != nil {
		return nil, err
	}
	if hasDebt {
		selected["noShowDebtKobo"] = debt
		destination["noShowDebtKobo"] = debt
	}
	if hasDue {
		selected["amountDueKobo"] = due
		destination["amountDueKobo"] = due
	}
	selectedJSON, err := json.Marshal(selected)
	if err != nil {
		return nil, err
	}
	destination["selectedFare"] = selectedJSON
	return json.Marshal(destination)
}

func (r *postgresDriverRepo) DeclineAssignment(ctx context.Context, tripID, driverID string) (*RetryTrip, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var payload []byte
	err = tx.QueryRow(ctx, `UPDATE driver_assignments SET status='declined',responded_at=NOW() WHERE trip_id=$1::UUID AND driver_id=$2::UUID AND status='offered' RETURNING trip_payload`, tripID, driverID).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("active assignment not found")
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE drivers SET status=CASE WHEN online_requested AND driver_balance_eligible(id,online_market_code) AND COALESCE((SELECT eligible_to_drive FROM driver_eligibility WHERE driver_id=drivers.id),FALSE) THEN 'available' ELSE 'offline' END,
		available=online_requested AND driver_balance_eligible(id,online_market_code) AND COALESCE((SELECT eligible_to_drive FROM driver_eligibility WHERE driver_id=drivers.id),FALSE),updated_at=NOW() WHERE id=$1::UUID`, driverID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE trips SET status='pending',assigned_driver_id=NULL,updated_at=NOW(),version=version+1 WHERE id=$1::UUID AND status='assigned'`, tripID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &RetryTrip{TripID: tripID, Payload: payload, PreviousDriverID: driverID}, nil
}

func (r *postgresDriverRepo) ExpireOffers(ctx context.Context, limit int) ([]RetryTrip, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT trip_id::TEXT,driver_id::TEXT,trip_payload FROM driver_assignments WHERE status='offered' AND expires_at<=NOW() ORDER BY expires_at FOR UPDATE SKIP LOCKED LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type expired struct {
		tripID, driverID string
		payload          []byte
	}
	var items []expired
	for rows.Next() {
		var item expired
		if err := rows.Scan(&item.tripID, &item.driverID, &item.payload); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	result := make([]RetryTrip, 0, len(items))
	for _, item := range items {
		if _, err := tx.Exec(ctx, `UPDATE driver_assignments SET status='timed_out',responded_at=NOW() WHERE trip_id=$1::UUID AND driver_id=$2::UUID AND status='offered'`, item.tripID, item.driverID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE drivers SET status=CASE WHEN online_requested AND driver_balance_eligible(id,online_market_code) AND COALESCE((SELECT eligible_to_drive FROM driver_eligibility WHERE driver_id=drivers.id),FALSE) THEN 'available' ELSE 'offline' END,
			available=online_requested AND driver_balance_eligible(id,online_market_code) AND COALESCE((SELECT eligible_to_drive FROM driver_eligibility WHERE driver_id=drivers.id),FALSE),updated_at=NOW() WHERE id=$1::UUID`, item.driverID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE trips SET status='pending',assigned_driver_id=NULL,updated_at=NOW(),version=version+1 WHERE id=$1::UUID AND status='assigned'`, item.tripID); err != nil {
			return nil, err
		}
		result = append(result, RetryTrip{TripID: item.tripID, Payload: item.payload, PreviousDriverID: item.driverID})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *postgresDriverRepo) get(ctx context.Context, id string) (*pb.Driver, error) {
	d := &pb.Driver{Location: &pb.Location{}}
	err := r.pool.QueryRow(ctx, `SELECT id::TEXT,name,profile_pic,car_plate,package_slug,COALESCE(ST_Y(location::geometry),0),COALESCE(ST_X(location::geometry),0) FROM drivers WHERE id=$1::UUID`, id).Scan(&d.Id, &d.Name, &d.ProfilePic, &d.CarPlate, &d.PackageSlug, &d.Location.Latitude, &d.Location.Longitude)
	if err != nil {
		return nil, fmt.Errorf("get driver: %w", err)
	}
	return d, nil
}
