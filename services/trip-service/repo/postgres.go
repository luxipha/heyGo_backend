package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/services/trip-service/types"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	pb "github.com/luxipha/heyGo_backend/shared/proto/trip"
	"github.com/luxipha/heyGo_backend/shared/tripstate"
	sharedtypes "github.com/luxipha/heyGo_backend/shared/types"
)

type postgresTripRepo struct{ pool *pgxpool.Pool }

var ErrOfferNotActive = errors.New("driver is not the active assignee for trip")
var ErrFareExpired = errors.New("fare_expired")
var ErrFareForeign = errors.New("fare_foreign")
var ErrFareInvalid = errors.New("fare_invalid")
var ErrDebtChanged = errors.New("debt_changed")
var ErrActiveRiderTrip = errors.New("rider already has an active trip")
var ErrRiderRestricted = errors.New("rider is restricted from requesting new trips")
var ErrPricingUnavailable = errors.New("published pricing is unavailable or market classification is ambiguous")

func NewPostgresRepository(pool *pgxpool.Pool) TripRepo { return &postgresTripRepo{pool: pool} }

func (r *postgresTripRepo) PricingForQuote(ctx context.Context, latitude, longitude float64) (*QuotePricing, error) {
	var market string
	err := r.pool.QueryRow(ctx, `SELECT CASE WHEN COUNT(*)=1 THEN MIN(code) ELSE NULL END
		FROM trip_geofences WHERE kind='market' AND status='approved'
		AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW())
		AND ST_Covers(boundary,ST_SetSRID(ST_MakePoint($1,$2),4326))`, longitude, latitude).Scan(&market)
	if err != nil || market == "" {
		return nil, ErrPricingUnavailable
	}
	var id string
	err = r.pool.QueryRow(ctx, `SELECT CASE WHEN COUNT(*)=1 THEN MIN(id::TEXT) ELSE NULL END
		FROM pricing_versions WHERE market_code=$1 AND status='approved'
		AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW())`, market).Scan(&id)
	if err != nil || id == "" {
		return nil, ErrPricingUnavailable
	}
	rows, err := r.pool.Query(ctx, `SELECT r.package_slug,r.base_kobo,r.per_km_kobo,r.per_minute_kobo FROM pricing_package_rates r JOIN ride_categories c ON c.slug=r.package_slug AND c.status='active' WHERE pricing_id=$1::UUID ORDER BY r.package_slug`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pricing := &QuotePricing{ID: id, MarketCode: market, Rates: make([]PackageRate, 0, 4)}
	for rows.Next() {
		var rate PackageRate
		if err = rows.Scan(&rate.PackageSlug, &rate.BaseKobo, &rate.PerKmKobo, &rate.PerMinuteKobo); err != nil {
			return nil, err
		}
		pricing.Rates = append(pricing.Rates, rate)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	if len(pricing.Rates) == 0 {
		return nil, ErrPricingUnavailable
	}
	return pricing, nil
}

func (r *postgresTripRepo) Create(ctx context.Context, trip *types.TripModel) (*types.TripModel, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin trip creation: %w", err)
	}
	defer tx.Rollback(ctx)
	// Serialize trip creation for this Rider. ReadCommitted observes a competing
	// creation after the user row lock is acquired, avoiding snapshot races.
	var riderID string
	if err := tx.QueryRow(ctx, `SELECT id::TEXT FROM users WHERE id=$1::UUID AND active=TRUE AND roles @> ARRAY['rider']::TEXT[] FOR UPDATE`, trip.RiderID).Scan(&riderID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrRiderRestricted
		}
		return nil, err
	}
	var owner, consumed string
	var expires time.Time
	var consumedAt *time.Time
	var debt int64
	err = tx.QueryRow(ctx, `SELECT rider_id::TEXT,COALESCE(consumed_trip_id::TEXT,''),expires_at,no_show_debt_kobo,consumed_at FROM ride_fares WHERE id=$1::UUID FOR UPDATE`, trip.RideFare.ID).Scan(&owner, &consumed, &expires, &debt, &consumedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrFareInvalid
	}
	if err != nil {
		return nil, err
	}
	if owner != trip.RiderID {
		return nil, ErrFareForeign
	}
	if consumed != "" {
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		existing, err := r.GetByID(ctx, consumed)
		if err != nil {
			return nil, err
		}
		if existing.RiderID != trip.RiderID || existing.RideFare == nil || existing.RideFare.ID != trip.RideFare.ID {
			return nil, ErrFareInvalid
		}
		return existing, nil
	}
	if consumedAt != nil {
		return nil, ErrFareInvalid
	}
	// A previously issued quote may not start a new trip after its category is
	// deactivated. Already consumed quotes return their original trip above.
	var categoryActive bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ride_fares f JOIN ride_categories c ON c.slug=f.package_slug WHERE f.id=$1::UUID AND c.status='active')`, trip.RideFare.ID).Scan(&categoryActive); err != nil {
		return nil, err
	}
	if !categoryActive {
		return nil, ErrPricingUnavailable
	}
	if !expires.After(time.Now()) {
		return nil, ErrFareExpired
	}
	var currentDebt int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(d.amount_kobo),0)::BIGINT FROM rider_no_show_debts d WHERE d.rider_id=$1::UUID AND d.status='due' AND NOT EXISTS(SELECT 1 FROM trip_no_show_debt_settlements s WHERE s.debt_id=d.id AND s.status IN ('pending','settled'))`, trip.RiderID).Scan(&currentDebt); err != nil {
		return nil, err
	}
	if currentDebt != debt || debt != trip.RideFare.NoShowDebtKobo {
		return nil, ErrDebtChanged
	}
	classifiedAt := time.Now().UTC()
	classification, err := classifyTrip(ctx, tx, trip.RideFare, classifiedAt)
	if err != nil {
		return nil, fmt.Errorf("classify trip: %w", err)
	}
	if trip.RideFare.PricingMarketCode != "" && trip.RideFare.PricingMarketCode != classification.MarketCode {
		return nil, ErrPricingUnavailable
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM trips t LEFT JOIN trip_settlements s ON s.trip_id=t.id WHERE t.rider_id=$1::UUID AND `+tripstate.RiderRecoverableSQL+`)`, trip.RiderID).Scan(&active); err != nil {
		return nil, err
	}
	if active {
		return nil, ErrActiveRiderTrip
	}
	_, err = tx.Exec(ctx, `INSERT INTO trips (id,rider_id,ride_fare_id,status,created_at,updated_at,
		market_code,origin_region_code,destination_region_code,regulatory_tags,classification_geofences,classified_at,no_show_debt_kobo,matching_deadline)
		VALUES ($1::UUID,$2::UUID,$3::UUID,$4,$5,$5,$6,$7,$8,$9,$10::JSONB,$5,$11,$5::TIMESTAMPTZ+INTERVAL '2 minutes')`,
		trip.ID, trip.RiderID, trip.RideFare.ID, trip.Status, classifiedAt,
		classification.MarketCode, classification.OriginRegionCode, classification.DestinationRegionCode,
		classification.Tags, classification.Geofences, trip.RideFare.NoShowDebtKobo)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Message == "rider_trip_restricted" {
			return nil, ErrRiderRestricted
		}
		return nil, fmt.Errorf("create trip: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ride_fares SET consumed_trip_id=$2::UUID,consumed_at=NOW() WHERE id=$1::UUID`, trip.RideFare.ID, trip.ID); err != nil {
		return nil, err
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
	err = tx.QueryRow(ctx, `INSERT INTO ride_fares (id,rider_id,package_slug,total_fare_minor,route,pickup,destination,no_show_debt_kobo,pricing_version_id,pricing_market_code,preview_id)
		VALUES ($1::UUID,$2::UUID,$3,$4,$5,
		CASE WHEN $6::DOUBLE PRECISION IS NULL THEN NULL ELSE ST_SetSRID(ST_MakePoint($7::DOUBLE PRECISION,$6::DOUBLE PRECISION),4326)::geography END,
		CASE WHEN $8::DOUBLE PRECISION IS NULL THEN NULL ELSE ST_SetSRID(ST_MakePoint($9::DOUBLE PRECISION,$8::DOUBLE PRECISION),4326)::geography END,0,NULLIF($10,'')::UUID,$11,COALESCE(NULLIF($12,'')::UUID,gen_random_uuid())) RETURNING preview_id::TEXT,expires_at`,
		fare.ID, fare.RiderID, fare.PackageSlug, fare.TotalFareInPaise, route, pickupLat, pickupLng, destinationLat, destinationLng, fare.PricingVersionID, fare.PricingMarketCode, fare.PreviewID).Scan(&fare.PreviewID, &fare.ExpiresAt)
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
	err := r.pool.QueryRow(ctx, `SELECT id::TEXT,rider_id::TEXT,package_slug,total_fare_minor,route,no_show_debt_kobo,COALESCE(pricing_version_id::TEXT,''),COALESCE(pricing_market_code,''),
		ST_Y(pickup::geometry),ST_X(pickup::geometry),ST_Y(destination::geometry),ST_X(destination::geometry),preview_id::TEXT,expires_at,COALESCE(consumed_trip_id::TEXT,'')
		FROM ride_fares WHERE id=$1::UUID`, fareID).Scan(&fare.ID, &fare.RiderID, &fare.PackageSlug, &fare.TotalFareInPaise, &route, &fare.NoShowDebtKobo, &fare.PricingVersionID, &fare.PricingMarketCode, &pickupLat, &pickupLng, &destinationLat, &destinationLng, &fare.PreviewID, &fare.ExpiresAt, &fare.ConsumedTripID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrFareInvalid
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
		  AND d.id=a.driver_id AND d.online_requested=TRUE AND d.status='offered' AND driver_home_matches(d.id,t.id)
		  AND a.status='offered' AND a.expires_at>NOW() AND COALESCE(t.matching_deadline,t.created_at+INTERVAL '2 minutes')>NOW() AND t.status IN ('pending','assigned')`, tripID, driver.Id)
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
		if err := enqueueTripTrustEvents(ctx, tx, tripID, "completed", ""); err != nil {
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
		if err := enqueueTripTrustEvents(ctx, tx, tripID, "cancelled", actorID); err != nil {
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
	rows, err := r.pool.Query(ctx, `SELECT COALESCE(u.casper_id_user_id,''),'consumer' FROM trips t JOIN users u ON u.id=t.rider_id WHERE t.id=$1::UUID UNION ALL SELECT COALESCE(u.casper_id_user_id,''),'provider' FROM trips t JOIN users u ON u.id=t.assigned_driver_id WHERE t.id=$1::UUID`, tripID)
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
		COALESCE(CASE WHEN t.rider_id=$2::UUID THEN du.casper_id_user_id ELSE ru.casper_id_user_id END,''),
	t.rider_id=$2::UUID
	FROM trips t JOIN users ru ON ru.id=t.rider_id JOIN users du ON du.id=t.assigned_driver_id
	WHERE t.id=$1::UUID AND t.status='completed' AND $2::UUID IN(t.rider_id,t.assigned_driver_id)`, tripID, actorID).Scan(&subjectID, &subject.Role, &subject.CasperID, &actorIsRider)
	if errors.Is(err, pgx.ErrNoRows) {
		return TrustSubject{}, false, fmt.Errorf("completed trip is not accessible to this user")
	}
	if err != nil {
		return TrustSubject{}, false, err
	}
	if subjectID == actorID {
		return TrustSubject{}, false, fmt.Errorf("completed trip is not accessible to this user")
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
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return TrustSubject{}, false, fmt.Errorf("begin rating: %w", err)
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `INSERT INTO trip_ratings(trip_id,actor_id,subject_id,rating,feedback_tags,comment,admin_review_status)
		VALUES($1::UUID,$2::UUID,$3::UUID,$4,$5,$6,$7) ON CONFLICT(trip_id,actor_id) DO NOTHING`, tripID, actorID, subjectID, rating, feedbackTags, comment, reviewStatus)
	if err != nil {
		return TrustSubject{}, false, fmt.Errorf("save rating: %w", err)
	}
	if result.RowsAffected() == 1 {
		if err := enqueueTrustEventTx(ctx, tx, subject.CasperID, subject.Role, "rating.submitted", "trip_"+tripID+"_"+map[bool]string{true: "rider_rating_driver", false: "driver_rating_rider"}[actorIsRider], tripID, &rating, time.Now().UTC()); err != nil {
			return TrustSubject{}, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return TrustSubject{}, false, fmt.Errorf("commit rating: %w", err)
	}
	return subject, result.RowsAffected() == 1, nil
}

func enqueueTripTrustEvents(ctx context.Context, tx pgx.Tx, tripID, outcome, actorID string) error {
	if outcome == "completed" {
		for _, role := range []string{"consumer", "provider"} {
			if err := enqueueTrustParticipantEvent(ctx, tx, tripID, role, "interaction.completed", "trip_"+tripID+"_"+role+"_completed", nil); err != nil {
				return err
			}
		}
		return nil
	}
	role := "consumer"
	if actorID != "" {
		var riderID string
		if err := tx.QueryRow(ctx, `SELECT rider_id::TEXT FROM trips WHERE id=$1::UUID`, tripID).Scan(&riderID); err != nil {
			return err
		}
		if actorID != riderID {
			role = "provider"
		}
	}
	return enqueueTrustParticipantEvent(ctx, tx, tripID, role, "interaction.cancelled_by_"+role, "trip_"+tripID+"_"+role+"_cancelled", nil)
}

func enqueueTrustParticipantEvent(ctx context.Context, tx pgx.Tx, tripID, role, eventType, externalID string, rating *int) error {
	_, err := tx.Exec(ctx, `INSERT INTO casperid_trust_deliveries(external_event_id,casper_id_user_id,actor_role,event_type,interaction_reference,rating,occurred_at)
		SELECT $2,u.casper_id_user_id,$3,$4,$1::UUID::TEXT,$5,NOW() FROM trips t JOIN users u ON u.id=CASE WHEN $3='consumer' THEN t.rider_id ELSE t.assigned_driver_id END
		WHERE t.id=$1::UUID AND u.casper_id_user_id<>'' ON CONFLICT(external_event_id) DO NOTHING`, tripID, externalID, role, eventType, rating)
	if err != nil {
		return fmt.Errorf("enqueue CasperID trust event: %w", err)
	}
	return nil
}

func enqueueTrustEventTx(ctx context.Context, tx pgx.Tx, casperID, role, eventType, externalID, interactionReference string, rating *int, occurredAt time.Time) error {
	if strings.TrimSpace(casperID) == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO casperid_trust_deliveries(external_event_id,casper_id_user_id,actor_role,event_type,interaction_reference,rating,occurred_at)
		VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(external_event_id) DO NOTHING`, externalID, casperID, role, eventType, interactionReference, rating, occurredAt)
	if err != nil {
		return fmt.Errorf("enqueue CasperID trust event: %w", err)
	}
	return nil
}
