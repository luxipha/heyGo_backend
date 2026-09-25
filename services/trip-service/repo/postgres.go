package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/luxipha/heyGo_backend/services/trip-service/types"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	pb "github.com/luxipha/heyGo_backend/shared/proto/trip"
	sharedtypes "github.com/luxipha/heyGo_backend/shared/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type postgresTripRepo struct{ pool *pgxpool.Pool }

var ErrOfferNotActive = errors.New("driver is not the active assignee for trip")

func NewPostgresRepository(pool *pgxpool.Pool) TripRepo { return &postgresTripRepo{pool: pool} }

func (r *postgresTripRepo) Create(ctx context.Context, trip *types.TripModel) (*types.TripModel, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, fmt.Errorf("begin trip creation: %w", err)
	}
	defer tx.Rollback(ctx)
	classifiedAt := time.Now().UTC()
	classification, err := classifyTrip(ctx, tx, trip.RideFare, classifiedAt)
	if err != nil {
		return nil, fmt.Errorf("classify trip: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO trips (id,rider_id,ride_fare_id,status,created_at,updated_at,
		market_code,origin_region_code,destination_region_code,regulatory_tags,classification_geofences,classified_at,no_show_debt_kobo)
		VALUES ($1::UUID,$2::UUID,$3::UUID,$4,$5,$5,$6,$7,$8,$9,$10::JSONB,$5,$11)`,
		trip.ID, trip.RiderID, trip.RideFare.ID, trip.Status, classifiedAt,
		classification.MarketCode, classification.OriginRegionCode, classification.DestinationRegionCode,
		classification.Tags, classification.Geofences, trip.RideFare.NoShowDebtKobo)
	if err != nil {
		return nil, fmt.Errorf("create trip: %w", err)
	}
	if trip.RideFare.NoShowDebtKobo > 0 {
		var fareCreatedAt time.Time
		if err := tx.QueryRow(ctx, `SELECT created_at FROM ride_fares WHERE id=$1::UUID AND rider_id=$2::UUID`, trip.RideFare.ID, trip.RiderID).Scan(&fareCreatedAt); err != nil {
			return nil, fmt.Errorf("read no-show debt fare snapshot: %w", err)
		}
		rows, err := tx.Query(ctx, `SELECT d.id::TEXT,d.beneficiary_driver_id::TEXT,d.amount_kobo FROM rider_no_show_debts d
			WHERE d.rider_id=$1::UUID AND d.status='due' AND d.created_at<=$2
			AND NOT EXISTS(SELECT 1 FROM trip_no_show_debt_settlements s WHERE s.debt_id=d.id AND s.status IN ('pending','settled'))
			ORDER BY d.created_at,d.id FOR UPDATE OF d`, trip.RiderID, fareCreatedAt)
		if err != nil {
			return nil, fmt.Errorf("lock rider no-show debts: %w", err)
		}
		type debtAllocation struct {
			id, beneficiary string
			amount          int64
		}
		allocations := []debtAllocation{}
		var total int64
		for rows.Next() {
			var d debtAllocation
			if err := rows.Scan(&d.id, &d.beneficiary, &d.amount); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan rider no-show debt: %w", err)
			}
			allocations = append(allocations, d)
			total += d.amount
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read rider no-show debts: %w", err)
		}
		rows.Close()
		if total != trip.RideFare.NoShowDebtKobo {
			return nil, fmt.Errorf("rider no-show debt changed after fare preview")
		}
		for _, d := range allocations {
			if _, err := tx.Exec(ctx, `INSERT INTO trip_no_show_debt_settlements(trip_id,debt_id,beneficiary_driver_id,amount_kobo,status)
			VALUES($1::UUID,$2::UUID,$3::UUID,$4,'pending')`, trip.ID, d.id, d.beneficiary, d.amount); err != nil {
				return nil, fmt.Errorf("reserve rider no-show debt: %w", err)
			}
		}
	}
	if err := enqueueTripEvent(ctx, tx, trip, contracts.TripEventCreated); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit trip creation: %w", err)
	}
	return r.GetByID(ctx, trip.ID)
}

func (r *postgresTripRepo) SaveRideFare(ctx context.Context, fare *types.RideFareModel) error {
	route, err := json.Marshal(fare.Route)
	if err != nil {
		return fmt.Errorf("encode ride fare route: %w", err)
	}
	var pickupLat, pickupLng, destinationLat, destinationLng any
	if fare.Pickup != nil {
		pickupLat, pickupLng = fare.Pickup.Latitude, fare.Pickup.Longitude
	}
	if fare.Destination != nil {
		destinationLat, destinationLng = fare.Destination.Latitude, fare.Destination.Longitude
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin save ride fare: %w", err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO ride_fares (id,rider_id,package_slug,total_fare_minor,route,pickup,destination,no_show_debt_kobo)
		VALUES ($1::UUID,$2::UUID,$3,$4,$5,
		CASE WHEN $6::DOUBLE PRECISION IS NULL THEN NULL ELSE ST_SetSRID(ST_MakePoint($7::DOUBLE PRECISION,$6::DOUBLE PRECISION),4326)::geography END,
		CASE WHEN $8::DOUBLE PRECISION IS NULL THEN NULL ELSE ST_SetSRID(ST_MakePoint($9::DOUBLE PRECISION,$8::DOUBLE PRECISION),4326)::geography END,0)`,
		fare.ID, fare.RiderID, fare.PackageSlug, fare.TotalFareInPaise, route, pickupLat, pickupLng, destinationLat, destinationLng)
	if err != nil {
		return fmt.Errorf("save ride fare: %w", err)
	}
	var debt int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(SUM(d.amount_kobo),0)::BIGINT FROM rider_no_show_debts d
		WHERE d.rider_id=$1::UUID AND d.status='due' AND d.created_at<=(SELECT created_at FROM ride_fares WHERE id=$2::UUID)
		AND NOT EXISTS(SELECT 1 FROM trip_no_show_debt_settlements s WHERE s.debt_id=d.id AND s.status IN ('pending','settled'))`, fare.RiderID, fare.ID).Scan(&debt); err != nil {
		return fmt.Errorf("read rider no-show debt for fare: %w", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE ride_fares SET no_show_debt_kobo=$2 WHERE id=$1::UUID`, fare.ID, debt); err != nil {
		return fmt.Errorf("snapshot rider no-show debt for fare: %w", err)
	}
	fare.NoShowDebtKobo = debt
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit saved ride fare: %w", err)
	}
	return nil
}

func (r *postgresTripRepo) GetRideFareByID(ctx context.Context, fareID string) (*types.RideFareModel, error) {
	var fare types.RideFareModel
	var route []byte
	var pickupLat, pickupLng, destinationLat, destinationLng *float64
	err := r.pool.QueryRow(ctx, `SELECT id::TEXT,rider_id::TEXT,package_slug,total_fare_minor,route,no_show_debt_kobo,
		ST_Y(pickup::geometry),ST_X(pickup::geometry),ST_Y(destination::geometry),ST_X(destination::geometry)
		FROM ride_fares WHERE id=$1::UUID AND expires_at>NOW()`, fareID).Scan(&fare.ID, &fare.RiderID, &fare.PackageSlug, &fare.TotalFareInPaise, &route, &fare.NoShowDebtKobo, &pickupLat, &pickupLng, &destinationLat, &destinationLng)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get ride fare: %w", err)
	}
	if err := json.Unmarshal(route, &fare.Route); err != nil {
		return nil, fmt.Errorf("decode ride fare route: %w", err)
	}
	if pickupLat != nil && pickupLng != nil {
		fare.Pickup = &sharedtypes.Coordinate{Latitude: *pickupLat, Longitude: *pickupLng}
	}
	if destinationLat != nil && destinationLng != nil {
		fare.Destination = &sharedtypes.Coordinate{Latitude: *destinationLat, Longitude: *destinationLng}
	}
	return &fare, nil
}

func (r *postgresTripRepo) GetByID(ctx context.Context, tripID string) (*types.TripModel, error) {
	return scanTrip(r.pool.QueryRow(ctx, tripSelect+` WHERE t.id=$1::UUID`, tripID))
}

func (r *postgresTripRepo) UpdateWithDriver(ctx context.Context, tripID string, driver *pb.TripDriver) (*types.TripModel, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin accept trip: %w", err)
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `
		UPDATE trips t SET assigned_driver_id=$2::UUID,status='accepted',updated_at=NOW(),version=version+1
		FROM driver_assignments a, drivers d
		WHERE t.id=$1::UUID AND a.trip_id=t.id AND a.driver_id=$2::UUID
		  AND d.id=a.driver_id AND d.online_requested=TRUE AND d.status='offered'
		  AND a.status='offered' AND a.expires_at>NOW() AND t.status IN ('pending','assigned')`, tripID, driver.Id)
	if err != nil {
		return nil, fmt.Errorf("accept trip: %w", err)
	}
	if result.RowsAffected() != 1 {
		var existingStatus, existingDriver string
		if checkErr := tx.QueryRow(ctx, `SELECT status,assigned_driver_id::TEXT FROM trips WHERE id=$1::UUID`, tripID).Scan(&existingStatus, &existingDriver); checkErr == nil && existingDriver == driver.Id && (existingStatus == "accepted" || existingStatus == "arrived" || existingStatus == "started") {
			_ = tx.Rollback(ctx)
			return r.GetByID(ctx, tripID)
		}
		return nil, ErrOfferNotActive
	}
	if _, err := tx.Exec(ctx, `UPDATE driver_assignments SET status='accepted',responded_at=NOW() WHERE trip_id=$1::UUID AND driver_id=$2::UUID AND status='offered'`, tripID, driver.Id); err != nil {
		return nil, fmt.Errorf("accept assignment: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE drivers SET status='on_trip',available=FALSE,updated_at=NOW() WHERE id=$1::UUID`, driver.Id); err != nil {
		return nil, fmt.Errorf("reserve accepted driver: %w", err)
	}
	accepted, err := scanTrip(tx.QueryRow(ctx, tripSelect+` WHERE t.id=$1::UUID`, tripID))
	if err != nil {
		return nil, err
	}
	if err := enqueueTripEvent(ctx, tx, accepted, contracts.TripEventDriverAssigned); err != nil {
		return nil, err
	}
	if err := enqueueAcceptedAck(ctx, tx, accepted); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit accept trip: %w", err)
	}
	return r.GetByID(ctx, tripID)
}

func (r *postgresTripRepo) Start(ctx context.Context, tripID, actorID string) (*types.TripModel, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE trips SET status='started',started_at=NOW(),updated_at=NOW(),version=version+1 WHERE id=$1::UUID AND assigned_driver_id=$2::UUID AND status IN ('accepted','arrived')`, tripID, actorID)
	if err != nil {
		return nil, false, fmt.Errorf("start trip: %w", err)
	}
	if result.RowsAffected() == 1 {
		started, err := scanTrip(tx.QueryRow(ctx, tripSelect+` WHERE t.id=$1::UUID`, tripID))
		if err != nil {
			return nil, false, err
		}
		if err := enqueueTripEvent(ctx, tx, started, contracts.TripEventStarted); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	trip, err := r.GetByID(ctx, tripID)
	if err != nil {
		return nil, false, err
	}
	if trip.Driver == nil || trip.Driver.Id != actorID || (result.RowsAffected() == 0 && trip.Status != "started") {
		return nil, false, fmt.Errorf("trip cannot be started by this user")
	}
	return trip, result.RowsAffected() == 1, nil
}

func (r *postgresTripRepo) Arrive(ctx context.Context, tripID, actorID string) (*types.TripModel, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin trip arrival: %w", err)
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE trips SET status='arrived',arrived_at=NOW(),updated_at=NOW(),version=version+1
		WHERE id=$1::UUID AND assigned_driver_id=$2::UUID AND status='accepted'`, tripID, actorID)
	if err != nil {
		return nil, false, fmt.Errorf("mark trip arrival: %w", err)
	}
	if result.RowsAffected() == 1 {
		arrived, err := scanTrip(tx.QueryRow(ctx, tripSelect+` WHERE t.id=$1::UUID`, tripID))
		if err != nil {
			return nil, false, err
		}
		if err := enqueueTripEvent(ctx, tx, arrived, contracts.TripEventArrived); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit trip arrival: %w", err)
	}
	trip, err := r.GetByID(ctx, tripID)
	if err != nil {
		return nil, false, err
	}
	if trip.Driver == nil || trip.Driver.Id != actorID || (result.RowsAffected() == 0 && trip.Status != "arrived") {
		return nil, false, fmt.Errorf("trip cannot be marked arrived by this user")
	}
	return trip, result.RowsAffected() == 1, nil
}

const tripSelect = `SELECT t.id::TEXT,t.rider_id::TEXT,t.status,
	f.id::TEXT,f.rider_id::TEXT,f.package_slug,f.total_fare_minor,f.route,t.no_show_debt_kobo,
	COALESCE(t.assigned_driver_id::TEXT,''),COALESCE(d.name,''),COALESCE(d.profile_pic,''),COALESCE(d.car_plate,'')
	FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id LEFT JOIN drivers d ON d.id=t.assigned_driver_id`

type rowScanner interface{ Scan(...any) error }

func scanTrip(row rowScanner) (*types.TripModel, error) {
	var trip types.TripModel
	var fare types.RideFareModel
	var driver pb.TripDriver
	var route []byte
	err := row.Scan(&trip.ID, &trip.RiderID, &trip.Status, &fare.ID, &fare.RiderID, &fare.PackageSlug, &fare.TotalFareInPaise, &route, &fare.NoShowDebtKobo, &driver.Id, &driver.Name, &driver.ProfilePic, &driver.CarPlate)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan trip: %w", err)
	}
	if err := json.Unmarshal(route, &fare.Route); err != nil {
		return nil, fmt.Errorf("decode trip route: %w", err)
	}
	trip.RideFare = &fare
	trip.Driver = &driver
	return &trip, nil
}

func (r *postgresTripRepo) Complete(ctx context.Context, tripID, actorID string) (*types.TripModel, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE trips SET status='completed',completed_at=NOW(),updated_at=NOW(),version=version+1 WHERE id=$1::UUID AND assigned_driver_id=$2::UUID AND status='started'`, tripID, actorID)
	if err != nil {
		return nil, false, fmt.Errorf("complete trip: %w", err)
	}
	if result.RowsAffected() == 1 {
		if err := assessTripStatutoryCharges(ctx, tx, tripID, actorID); err != nil {
			return nil, false, fmt.Errorf("assess trip statutory charges: %w", err)
		}
		completed, err := scanTrip(tx.QueryRow(ctx, tripSelect+` WHERE t.id=$1::UUID`, tripID))
		if err != nil {
			return nil, false, err
		}
		if err := enqueueTripEvent(ctx, tx, completed, contracts.TripEventCompleted); err != nil {
			return nil, false, err
		}
		var riderID string
		var fareAmountKobo, noShowDebtKobo int64
		if err := tx.QueryRow(ctx, `SELECT t.rider_id::TEXT,ROUND(f.total_fare_minor)::BIGINT,t.no_show_debt_kobo FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id WHERE t.id=$1::UUID`, tripID).Scan(&riderID, &fareAmountKobo, &noShowDebtKobo); err != nil {
			return nil, false, fmt.Errorf("read completed trip settlement amount: %w", err)
		}
		amountKobo := fareAmountKobo + noShowDebtKobo
		if _, err := tx.Exec(ctx, `INSERT INTO trip_settlements(trip_id,driver_id,rider_id,expected_amount_kobo,fare_amount_kobo,no_show_debt_kobo,status)
			VALUES($1::UUID,$2::UUID,$3::UUID,$4,$5,$6,'pending') ON CONFLICT(trip_id) DO NOTHING`, tripID, actorID, riderID, amountKobo, fareAmountKobo, noShowDebtKobo); err != nil {
			return nil, false, fmt.Errorf("create pending trip settlement: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO trip_settlement_actions(trip_id,actor_id,action,idempotency_key)
			VALUES($1::UUID,$2::UUID,'pending',$3) ON CONFLICT(idempotency_key) DO NOTHING`, tripID, actorID, "trip:"+tripID+":settlement:pending"); err != nil {
			return nil, false, fmt.Errorf("record pending trip settlement: %w", err)
		}
		if err := enqueueSettlementEvent(ctx, tx, tripID, riderID, actorID, amountKobo, "pending", 0); err != nil {
			return nil, false, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO driver_trip_earnings(trip_id,driver_id,fare_kobo,commission_bps,commission_kobo,earnings_kobo,accrued_at)
			SELECT t.id,t.assigned_driver_id,ROUND(f.total_fare_minor)::BIGINT,0,0,ROUND(f.total_fare_minor)::BIGINT,t.completed_at
			FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id WHERE t.id=$1::UUID AND t.assigned_driver_id=$2::UUID AND t.status='completed'
			ON CONFLICT(trip_id) DO NOTHING`, tripID, actorID); err != nil {
			return nil, false, fmt.Errorf("accrue driver earnings: %w", err)
		}
		if noShowDebtKobo > 0 {
			var allocated int64
			if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(amount_kobo),0)::BIGINT FROM trip_no_show_debt_settlements WHERE trip_id=$1::UUID AND status='pending'`, tripID).Scan(&allocated); err != nil || allocated != noShowDebtKobo {
				return nil, false, fmt.Errorf("trip no-show debt allocation mismatch: allocated=%d expected=%d err=%v", allocated, noShowDebtKobo, err)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE drivers SET status=CASE WHEN online_requested AND driver_balance_eligible(id,online_market_code) AND COALESCE((SELECT eligible_to_drive FROM driver_eligibility WHERE driver_id=drivers.id),FALSE) THEN 'available' ELSE 'offline' END,
			available=online_requested AND driver_balance_eligible(id,online_market_code) AND COALESCE((SELECT eligible_to_drive FROM driver_eligibility WHERE driver_id=drivers.id),FALSE),updated_at=NOW() WHERE id=$1::UUID`, actorID); err != nil {
			return nil, false, err
		}
		if _, err := tx.Exec(ctx, `UPDATE driver_assignments SET status='completed',responded_at=NOW() WHERE trip_id=$1::UUID AND status='accepted'`, tripID); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	trip, err := r.GetByID(ctx, tripID)
	if err != nil {
		return nil, false, err
	}
	if trip.Driver == nil || trip.Driver.Id != actorID || (result.RowsAffected() == 0 && trip.Status != "completed") {
		return nil, false, fmt.Errorf("trip cannot be completed by this user")
	}
	return trip, result.RowsAffected() == 1, nil
}

func (r *postgresTripRepo) Cancel(ctx context.Context, tripID, actorID, reason string) (*types.TripModel, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE trips SET status='cancelled',cancellation_reason=$3,cancellation_actor_id=$2::UUID,cancelled_at=NOW(),updated_at=NOW(),version=version+1 WHERE id=$1::UUID AND (rider_id=$2::UUID OR assigned_driver_id=$2::UUID) AND status IN ('pending','assigned','accepted','arrived')`, tripID, actorID, reason)
	if err != nil {
		return nil, false, fmt.Errorf("cancel trip: %w", err)
	}
	if result.RowsAffected() == 1 {
		if _, err := tx.Exec(ctx, `UPDATE trip_no_show_debt_settlements SET status='released' WHERE trip_id=$1::UUID AND status='pending'`, tripID); err != nil {
			return nil, false, fmt.Errorf("release no-show debt reservation on cancellation: %w", err)
		}
		cancelled, err := scanTrip(tx.QueryRow(ctx, tripSelect+` WHERE t.id=$1::UUID`, tripID))
		if err != nil {
			return nil, false, err
		}
		if err := enqueueTripEvent(ctx, tx, cancelled, contracts.TripEventCancelled); err != nil {
			return nil, false, err
		}
		if _, err := tx.Exec(ctx, `UPDATE drivers SET status=CASE WHEN online_requested AND driver_balance_eligible(id,online_market_code) AND COALESCE((SELECT eligible_to_drive FROM driver_eligibility WHERE driver_id=drivers.id),FALSE) THEN 'available' ELSE 'offline' END,
			available=online_requested AND driver_balance_eligible(id,online_market_code) AND COALESCE((SELECT eligible_to_drive FROM driver_eligibility WHERE driver_id=drivers.id),FALSE),updated_at=NOW()
			WHERE id=(SELECT assigned_driver_id FROM trips WHERE id=$1::UUID)`, tripID); err != nil {
			return nil, false, err
		}
		if _, err := tx.Exec(ctx, `UPDATE driver_assignments SET status='cancelled',responded_at=NOW() WHERE trip_id=$1::UUID AND status IN ('offered','accepted')`, tripID); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	trip, err := r.GetByID(ctx, tripID)
	if err != nil {
		return nil, false, err
	}
	if (trip.RiderID != actorID && (trip.Driver == nil || trip.Driver.Id != actorID)) || (result.RowsAffected() == 0 && trip.Status != "cancelled") {
		return nil, false, fmt.Errorf("trip cannot be cancelled by this user")
	}
	return trip, result.RowsAffected() == 1, nil
}

func (r *postgresTripRepo) TrustParticipants(ctx context.Context, tripID string) ([]TrustSubject, error) {
	rows, err := r.pool.Query(ctx, `SELECT u.casper_id_user_id,'consumer' FROM trips t JOIN users u ON u.id=t.rider_id WHERE t.id=$1::UUID UNION ALL SELECT u.casper_id_user_id,'provider' FROM trips t JOIN users u ON u.id=t.assigned_driver_id WHERE t.id=$1::UUID`, tripID)
	if err != nil {
		return nil, fmt.Errorf("load trust participants: %w", err)
	}
	defer rows.Close()
	var subjects []TrustSubject
	for rows.Next() {
		var subject TrustSubject
		if err := rows.Scan(&subject.CasperID, &subject.Role); err != nil {
			return nil, err
		}
		subjects = append(subjects, subject)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(subjects) == 0 {
		return nil, ErrNotFound
	}
	return subjects, nil
}

func (r *postgresTripRepo) Rate(ctx context.Context, tripID, actorID string, rating int, feedbackTags []string, comment string) (TrustSubject, bool, error) {
	if rating < 1 || rating > 5 {
		return TrustSubject{}, false, fmt.Errorf("rating must be between 1 and 5")
	}
	if len([]rune(comment)) > 250 {
		return TrustSubject{}, false, fmt.Errorf("rider comment must be 250 characters or fewer")
	}
	if feedbackTags == nil {
		feedbackTags = []string{}
	}
	var subject TrustSubject
	var subjectID string
	var actorIsRider bool
	err := r.pool.QueryRow(ctx, `SELECT CASE WHEN t.rider_id=$2::UUID THEN t.assigned_driver_id::TEXT ELSE t.rider_id::TEXT END,
	CASE WHEN t.rider_id=$2::UUID THEN 'provider' ELSE 'consumer' END,
	CASE WHEN t.rider_id=$2::UUID THEN du.casper_id_user_id ELSE ru.casper_id_user_id END,
	t.rider_id=$2::UUID
	FROM trips t JOIN users ru ON ru.id=t.rider_id JOIN users du ON du.id=t.assigned_driver_id
	WHERE t.id=$1::UUID AND t.status='completed' AND $2::UUID IN(t.rider_id,t.assigned_driver_id)`, tripID, actorID).Scan(&subjectID, &subject.Role, &subject.CasperID, &actorIsRider)
	if errors.Is(err, pgx.ErrNoRows) {
		return TrustSubject{}, false, fmt.Errorf("completed trip is not accessible to this user")
	}
	if err != nil {
		return TrustSubject{}, false, err
	}
	if !actorIsRider && comment != "" {
		return TrustSubject{}, false, fmt.Errorf("only riders may submit written rating comments")
	}
	if actorIsRider && len(feedbackTags) > 0 {
		return TrustSubject{}, false, fmt.Errorf("rider ratings do not accept driver feedback tags")
	}
	reviewStatus := "not_required"
	if actorIsRider && rating == 1 {
		reviewStatus = "pending"
	}
	result, err := r.pool.Exec(ctx, `INSERT INTO trip_ratings(trip_id,actor_id,subject_id,rating,feedback_tags,comment,admin_review_status)
		VALUES($1::UUID,$2::UUID,$3::UUID,$4,$5,$6,$7) ON CONFLICT(trip_id,actor_id) DO NOTHING`, tripID, actorID, subjectID, rating, feedbackTags, comment, reviewStatus)
	if err != nil {
		return TrustSubject{}, false, fmt.Errorf("save rating: %w", err)
	}
	return subject, result.RowsAffected() == 1, nil
}
