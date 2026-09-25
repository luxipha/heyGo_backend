package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	gatewayauth "github.com/cprakhar/uber-clone/services/api-gateway/auth"
	grpcclient "github.com/cprakhar/uber-clone/services/api-gateway/grpc-client"
	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/cprakhar/uber-clone/shared/driverstate"
	"github.com/cprakhar/uber-clone/shared/messaging"
	driverpb "github.com/cprakhar/uber-clone/shared/proto/driver"
	"github.com/cprakhar/uber-clone/shared/storage"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type driverAPI struct {
	pool  *pgxpool.Pool
	files storage.ObjectStore
}

func registerDriverRoutes(authenticated *gin.RouterGroup, pool *pgxpool.Pool, files storage.ObjectStore) {
	api := &driverAPI{pool: pool, files: files}
	authenticated.POST("/routes/driver", gatewayauth.RequireRole("driver"), api.route)
	driver := authenticated.Group("/driver", gatewayauth.RequireRole("driver"))
	driver.GET("/profile", api.profile)
	driver.PATCH("/profile", api.updateProfile)
	driver.GET("/vehicle", api.vehicle)
	driver.PATCH("/vehicle", api.updateVehicle)
	driver.PUT("/vehicle-package", api.updatePackage)
	driver.GET("/onboarding", api.onboarding)
	driver.GET("/documents", api.documents)
	driver.POST("/documents/uploads", api.createDocumentUpload)
	driver.POST("/documents", api.submitDocument)
	driver.POST("/documents/:id/resubmit", api.submitDocument)
	driver.GET("/inspection", api.inspection)
	driver.GET("/dashboard", api.dashboard)
	driver.GET("/operating-balance", api.operatingBalance)
	driver.GET("/operating-balance/transactions", api.operatingBalanceTransactions)
	driver.POST("/operating-balance/topups", api.createOperatingTopup)
	driver.GET("/operating-balance/topups/:id", api.operatingTopup)
	driver.POST("/availability", api.availability)
	driver.GET("/trips", api.tripHistory)
	driver.GET("/reviews", api.driverReviews)
	driver.GET("/performance", api.driverPerformance)
	registerDriverSafetyContactRoutes(driver, api)
	registerDriverNotificationRoutes(driver, api)
	registerDriverPrivacyRoutes(driver, api)
	driver.GET("/trips/active", api.activeTrip)
	driver.GET("/trips/:tripID", api.tripDetail)
	driver.GET("/trips/:tripID/receipt", api.tripReceipt)
	driver.GET("/trips/:tripID/settlement", api.tripSettlement)
	driver.POST("/trips/:tripID/settlement/confirm", api.confirmTripSettlement)
	driver.POST("/trips/:tripID/settlement/disputes", api.disputeTripSettlement)
	registerNoShowDriverRoutes(driver, api)
	driver.GET("/events", api.events)
}

func driverID(ctx *gin.Context) string {
	user, _ := gatewayauth.CurrentUser(ctx)
	return user.ID
}

func driverError(ctx *gin.Context, status int, code, message string) {
	ctx.JSON(status, contracts.APIErrorResponse{Error: contracts.APIError{Code: code, Message: message}})
}

func (a *driverAPI) ensureProfile(ctx *gin.Context) bool {
	_, err := a.pool.Exec(ctx.Request.Context(), `INSERT INTO driver_profiles(driver_id) VALUES($1::UUID) ON CONFLICT DO NOTHING`, driverID(ctx))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "driver_profile_unavailable", "Driver profile is unavailable")
		return false
	}
	return true
}

func (a *driverAPI) profile(ctx *gin.Context) {
	if !a.ensureProfile(ctx) {
		return
	}
	var name, photo, adminStatus, adminReason string
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT display_name,photo_url,admin_status,admin_reason FROM driver_profiles WHERE driver_id=$1::UUID`, driverID(ctx)).Scan(&name, &photo, &adminStatus, &adminReason)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "driver_profile_unavailable", "Driver profile is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"displayName": name, "photoUrl": photo, "adminStatus": adminStatus, "adminReason": adminReason}})
}

func (a *driverAPI) updateProfile(ctx *gin.Context) {
	var body struct {
		DisplayName *string `json:"displayName"`
	}
	if err := ctx.ShouldBindJSON(&body); err != nil || body.DisplayName == nil || strings.TrimSpace(*body.DisplayName) == "" || len(*body.DisplayName) > 120 {
		driverError(ctx, http.StatusBadRequest, "invalid_profile", "A display name of at most 120 characters is required")
		return
	}
	if !a.ensureProfile(ctx) {
		return
	}
	_, err := a.pool.Exec(ctx.Request.Context(), `UPDATE driver_profiles SET display_name=$2,admin_status='pending',admin_reason='',admin_reviewed_at=NULL,updated_at=NOW() WHERE driver_id=$1::UUID`, driverID(ctx), strings.TrimSpace(*body.DisplayName))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "driver_profile_unavailable", "Driver profile could not be updated")
		return
	}
	if err := a.markOffline(ctx); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "availability_unavailable", "Driver availability could not be updated")
		return
	}
	a.profile(ctx)
}

func (a *driverAPI) vehicle(ctx *gin.Context) {
	var packageSlug, makeName, model, color, plate, reviewStatus, reviewReason string
	var year *int
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT package_slug,make,model,model_year,color,plate,review_status,review_reason FROM driver_vehicles WHERE driver_id=$1::UUID`, driverID(ctx)).Scan(&packageSlug, &makeName, &model, &year, &color, &plate, &reviewStatus, &reviewReason)
	if errors.Is(err, pgx.ErrNoRows) {
		ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"packageSlug": "", "make": "", "model": "", "year": nil, "color": "", "plate": "", "reviewStatus": "required"}})
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "vehicle_unavailable", "Vehicle is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"packageSlug": packageSlug, "make": makeName, "model": model, "year": year, "color": color, "plate": plate, "reviewStatus": reviewStatus, "reviewReason": reviewReason}})
}

func validPackage(value string) bool {
	switch value {
	case "bike", "auto", "sedan", "suv":
		return true
	}
	return false
}

func (a *driverAPI) updatePackage(ctx *gin.Context) {
	var body struct {
		PackageSlug string `json:"packageSlug"`
	}
	if ctx.ShouldBindJSON(&body) != nil || !validPackage(body.PackageSlug) {
		driverError(ctx, http.StatusBadRequest, "invalid_package", "A supported package is required")
		return
	}
	if !a.ensureProfile(ctx) {
		return
	}
	_, err := a.pool.Exec(ctx.Request.Context(), `INSERT INTO driver_vehicles(driver_id,package_slug) VALUES($1::UUID,$2)
		ON CONFLICT(driver_id) DO UPDATE SET package_slug=EXCLUDED.package_slug,review_status='pending',review_reason='',reviewed_at=NULL,updated_at=NOW()`, driverID(ctx), body.PackageSlug)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "vehicle_unavailable", "Vehicle package could not be updated")
		return
	}
	if err := a.invalidateApproval(ctx); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "review_unavailable", "Driver review state could not be updated")
		return
	}
	a.vehicle(ctx)
}

func (a *driverAPI) updateVehicle(ctx *gin.Context) {
	var body struct {
		Make  string `json:"make"`
		Model string `json:"model"`
		Year  *int   `json:"year"`
		Color string `json:"color"`
		Plate string `json:"plate"`
	}
	if ctx.ShouldBindJSON(&body) != nil || strings.TrimSpace(body.Make) == "" || strings.TrimSpace(body.Model) == "" || strings.TrimSpace(body.Plate) == "" || len(body.Plate) > 30 || body.Year == nil || *body.Year < 1900 || *body.Year > time.Now().Year()+1 {
		driverError(ctx, http.StatusBadRequest, "invalid_vehicle", "Make, model, year, and plate are required")
		return
	}
	if !a.ensureProfile(ctx) {
		return
	}
	result, err := a.pool.Exec(ctx.Request.Context(), `UPDATE driver_vehicles SET make=$2,model=$3,model_year=$4,color=$5,plate=$6,review_status='pending',review_reason='',reviewed_at=NULL,updated_at=NOW() WHERE driver_id=$1::UUID`, driverID(ctx), strings.TrimSpace(body.Make), strings.TrimSpace(body.Model), *body.Year, strings.TrimSpace(body.Color), strings.TrimSpace(body.Plate))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "vehicle_unavailable", "Vehicle could not be updated")
		return
	}
	if result.RowsAffected() == 0 {
		driverError(ctx, http.StatusConflict, "package_required", "Select a vehicle package first")
		return
	}
	if err := a.invalidateApproval(ctx); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "review_unavailable", "Driver review state could not be updated")
		return
	}
	a.vehicle(ctx)
}

func (a *driverAPI) markOffline(ctx *gin.Context) error {
	_, err := a.pool.Exec(ctx.Request.Context(), `UPDATE drivers SET status=CASE WHEN status='on_trip' THEN status ELSE 'offline' END,available=FALSE,online_requested=FALSE,updated_at=NOW() WHERE id=$1::UUID`, driverID(ctx))
	return err
}

func (a *driverAPI) invalidateApproval(ctx *gin.Context) error {
	_, err := a.pool.Exec(ctx.Request.Context(), `UPDATE driver_profiles SET admin_status='pending',admin_reason='',admin_reviewed_at=NULL,updated_at=NOW() WHERE driver_id=$1::UUID`, driverID(ctx))
	if err != nil {
		return err
	}
	return a.markOffline(ctx)
}

func (a *driverAPI) onboarding(ctx *gin.Context) {
	state, err := driverstate.Read(ctx.Request.Context(), a.pool, driverID(ctx))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "onboarding_unavailable", "Onboarding status is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: state})
}

type documentData struct {
	ID          *string    `json:"id"`
	Type        string     `json:"type"`
	Status      string     `json:"status"`
	Reason      string     `json:"reason"`
	SubmittedAt *time.Time `json:"submittedAt"`
	VerifiedAt  *time.Time `json:"verifiedAt"`
	ExpiresAt   *time.Time `json:"expiresAt"`
}

func (a *driverAPI) documents(ctx *gin.Context) {
	user, _ := gatewayauth.CurrentUser(ctx)
	var nin bool
	if err := a.pool.QueryRow(ctx.Request.Context(), `SELECT nin_verified FROM users WHERE id=$1::UUID`, user.ID).Scan(&nin); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "documents_unavailable", "Documents are unavailable")
		return
	}
	ninStatus := "required"
	if nin && user.Verified {
		ninStatus = "approved"
	}
	result := []documentData{{Type: "nin", Status: ninStatus, Reason: ""}}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT DISTINCT ON (type) id::TEXT,type,
		CASE WHEN status='approved' AND expires_at IS NOT NULL AND expires_at<=NOW() THEN 'expired' ELSE status END,
		rejection_reason,submitted_at,reviewed_at,expires_at
		FROM driver_documents WHERE driver_id=$1::UUID ORDER BY type,submitted_at DESC,id DESC`, user.ID)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "documents_unavailable", "Documents are unavailable")
		return
	}
	documents := map[string]documentData{}
	for rows.Next() {
		var doc documentData
		if err := rows.Scan(&doc.ID, &doc.Type, &doc.Status, &doc.Reason, &doc.SubmittedAt, &doc.VerifiedAt, &doc.ExpiresAt); err != nil {
			rows.Close()
			driverError(ctx, http.StatusServiceUnavailable, "documents_unavailable", "Documents are unavailable")
			return
		}
		documents[doc.Type] = doc
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		driverError(ctx, http.StatusServiceUnavailable, "documents_unavailable", "Documents are unavailable")
		return
	}
	rows.Close()
	for _, kind := range []string{"driver_license", "vehicle_license", "roadworthiness", "auto_insurance", "hackney_permit"} {
		if doc, ok := documents[kind]; ok {
			result = append(result, doc)
		} else {
			result = append(result, documentData{Type: kind, Status: "required"})
		}
	}
	var inspectionID *string
	var status string
	var inspectedAt *time.Time
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT id::TEXT,status,inspected_at FROM driver_inspections WHERE driver_id=$1::UUID ORDER BY created_at DESC,id DESC LIMIT 1`, user.ID).Scan(&inspectionID, &status, &inspectedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		status = "required"
	} else if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "documents_unavailable", "Documents are unavailable")
		return
	}
	result = append(result, documentData{ID: inspectionID, Type: "vehicle_inspection", Status: status, VerifiedAt: inspectedAt})
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: result})
}

func (a *driverAPI) inspection(ctx *gin.Context) {
	var id string
	var partnerName, locationName, status, reason string
	var reportReceivedAt, inspectedAt *time.Time
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT id::TEXT,partner_name,location_name,report_received_at,status,outcome_reason,inspected_at
		FROM driver_inspections WHERE driver_id=$1::UUID ORDER BY created_at DESC,id DESC LIMIT 1`, driverID(ctx)).Scan(&id, &partnerName, &locationName, &reportReceivedAt, &status, &reason, &inspectedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"status": "required"}})
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "inspection_unavailable", "Inspection is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"id": id, "partnerName": partnerName, "locationName": locationName, "reportReceivedAt": reportReceivedAt, "status": status, "reason": reason, "inspectedAt": inspectedAt}})
}

type activeTripData struct {
	ID             string          `json:"id"`
	Status         string          `json:"status"`
	RiderID        string          `json:"riderId"`
	FareID         string          `json:"fareId"`
	FareKobo       int64           `json:"fareKobo"`
	NoShowDebtKobo int64           `json:"noShowDebtKobo"`
	AmountDueKobo  int64           `json:"amountDueKobo"`
	Route          json.RawMessage `json:"route"`
	CreatedAt      time.Time       `json:"createdAt"`
	StartedAt      *time.Time      `json:"startedAt"`
	ArrivedAt      *time.Time      `json:"arrivedAt"`
}

func (a *driverAPI) readActiveTrip(ctx *gin.Context) (*activeTripData, error) {
	var trip activeTripData
	var route []byte
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT t.id::TEXT,t.status,t.rider_id::TEXT,t.ride_fare_id::TEXT,ROUND(f.total_fare_minor)::BIGINT,t.no_show_debt_kobo,ROUND(f.total_fare_minor)::BIGINT+t.no_show_debt_kobo,f.route,t.created_at,t.arrived_at,t.started_at
		FROM trips t JOIN ride_fares f ON f.id=t.ride_fare_id WHERE t.assigned_driver_id=$1::UUID AND t.status IN ('assigned','accepted','arrived','started')
		ORDER BY t.updated_at DESC LIMIT 1`, driverID(ctx)).Scan(&trip.ID, &trip.Status, &trip.RiderID, &trip.FareID, &trip.FareKobo, &trip.NoShowDebtKobo, &trip.AmountDueKobo, &route, &trip.CreatedAt, &trip.ArrivedAt, &trip.StartedAt)
	if err != nil {
		return nil, err
	}
	trip.Route = route
	return &trip, nil
}

func (a *driverAPI) activeTrip(ctx *gin.Context) {
	trip, err := a.readActiveTrip(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusServiceUnavailable, "trip_unavailable", "Active trip is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: trip})
}

func (a *driverAPI) dashboard(ctx *gin.Context) {
	state, err := driverstate.Read(ctx.Request.Context(), a.pool, driverID(ctx))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "dashboard_unavailable", "Dashboard is unavailable")
		return
	}
	trip, err := a.readActiveTrip(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusServiceUnavailable, "dashboard_unavailable", "Dashboard is unavailable")
		return
	}
	var status string
	var available, onlineRequested bool
	var latitude, longitude *float64
	var locationUpdatedAt *time.Time
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT status,available,online_requested,ST_Y(location::geometry),ST_X(location::geometry),last_seen_at FROM drivers WHERE id=$1::UUID`, driverID(ctx)).Scan(&status, &available, &onlineRequested, &latitude, &longitude, &locationUpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		status = "offline"
		available = false
	} else if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "dashboard_unavailable", "Dashboard is unavailable")
		return
	}
	if !state.EligibleToDrive && status != "on_trip" {
		status = "offline"
		available = false
	}
	var location any
	if latitude != nil && longitude != nil {
		location = gin.H{"latitude": *latitude, "longitude": *longitude, "updatedAt": locationUpdatedAt}
	}
	var offer any
	var offerTripID string
	var attempt int
	var expiresAt time.Time
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT trip_id::TEXT,attempt,expires_at FROM driver_assignments WHERE driver_id=$1::UUID AND status='offered' AND expires_at>NOW() ORDER BY offered_at DESC LIMIT 1`, driverID(ctx)).Scan(&offerTripID, &attempt, &expiresAt)
	if err == nil {
		offer = gin.H{"tripId": offerTripID, "attempt": attempt, "expiresAt": expiresAt}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusServiceUnavailable, "dashboard_unavailable", "Dashboard is unavailable")
		return
	}
	earnings, err := readTodayEarnings(ctx.Request.Context(), a.pool, driverID(ctx), time.Now())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "dashboard_unavailable", "Dashboard is unavailable")
		return
	}
	operatingBalance, err := readDriverOperatingBalance(ctx.Request.Context(), a.pool, driverID(ctx))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "dashboard_unavailable", "Dashboard is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"onboarding": state, "eligibleToDrive": state.EligibleToDrive, "availability": gin.H{"status": status, "available": available, "onlineRequested": onlineRequested}, "map": gin.H{"driverLocation": location}, "activeOffer": offer, "activeTrip": trip, "todayEarnings": earnings, "operatingBalance": operatingBalance}})
}

func (a *driverAPI) availability(ctx *gin.Context) {
	var body struct {
		Available *bool `json:"available"`
	}
	if ctx.ShouldBindJSON(&body) != nil || body.Available == nil {
		driverError(ctx, http.StatusBadRequest, "invalid_availability", "available must be true or false")
		return
	}
	state, err := driverstate.Read(ctx.Request.Context(), a.pool, driverID(ctx))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "availability_unavailable", "Availability is unavailable")
		return
	}
	if *body.Available && !state.EligibleToDrive {
		driverError(ctx, http.StatusForbidden, "driver_not_eligible", "Complete all seven requirements and obtain admin approval before going online")
		return
	}
	if *body.Available {
		present, err := a.driverSocketPresent(ctx)
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "availability_unavailable", "Socket presence is unavailable")
			return
		}
		if !present {
			driverError(ctx, http.StatusConflict, "socket_required", "Connect the driver socket before going online")
			return
		}
		var market *string
		var locationRecordedAt time.Time
		err = a.pool.QueryRow(ctx.Request.Context(), `SELECT driver_market_at(location),recorded_at FROM driver_live_locations WHERE driver_id=$1::UUID`, driverID(ctx)).Scan(&market, &locationRecordedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			driverError(ctx, http.StatusConflict, "location_required", "Share your current location before going online")
			return
		}
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "availability_unavailable", "Availability is unavailable")
			return
		}
		if time.Since(locationRecordedAt) > 30*time.Minute {
			driverError(ctx, http.StatusConflict, "location_stale", "Share your current location before going online")
			return
		}
		if market == nil {
			driverError(ctx, http.StatusConflict, "market_unavailable", "Unable to go online in this area right now")
			return
		}
		var policyCount int
		var minimum, balance int64
		err = a.pool.QueryRow(ctx.Request.Context(), `SELECT COUNT(*),COALESCE(MIN(minimum_kobo),0),
			COALESCE((SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=$1::UUID),0)
			FROM operating_balance_policies WHERE market_code=$2 AND status='approved'
			AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW())`, driverID(ctx), *market).Scan(&policyCount, &minimum, &balance)
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "availability_unavailable", "Availability is unavailable")
			return
		}
		if policyCount != 1 {
			driverError(ctx, http.StatusConflict, "market_unavailable", "Unable to go online in this area right now")
			return
		}
		if balance < minimum {
			driverError(ctx, http.StatusConflict, "operating_balance_low", "Top up your operating balance to continue driving")
			return
		}
	}
	client, err := grpcclient.NewDriverServiceClient()
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "driver_service_unavailable", "Driver service is unavailable")
		return
	}
	defer client.Close()
	if *body.Available {
		var packageSlug string
		if err := a.pool.QueryRow(ctx.Request.Context(), `SELECT package_slug FROM driver_vehicles WHERE driver_id=$1::UUID`, driverID(ctx)).Scan(&packageSlug); err != nil || packageSlug == "" {
			driverError(ctx, http.StatusConflict, "package_required", "Vehicle package is required")
			return
		}
		if _, err := client.Client.RegisterDriver(ctx.Request.Context(), &driverpb.RegisterDriverRequest{DriverID: driverID(ctx), PackageSlug: packageSlug}); err != nil {
			driverError(ctx, http.StatusForbidden, "driver_not_eligible", "Driver could not go online")
			return
		}
		present, err := a.driverSocketPresent(ctx)
		if err != nil || !present {
			_, _ = client.Client.UnregisterDriver(ctx.Request.Context(), &driverpb.RegisterDriverRequest{DriverID: driverID(ctx)})
			if err != nil {
				driverError(ctx, http.StatusServiceUnavailable, "availability_unavailable", "Socket presence is unavailable")
				return
			}
			driverError(ctx, http.StatusConflict, "socket_required", "Reconnect the driver socket before going online")
			return
		}
	} else {
		if _, err := client.Client.UnregisterDriver(ctx.Request.Context(), &driverpb.RegisterDriverRequest{DriverID: driverID(ctx)}); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "availability_unavailable", "Driver could not go offline")
			return
		}
	}
	var status string
	var available, onlineRequested bool
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT status,available,online_requested FROM drivers WHERE id=$1::UUID`, driverID(ctx)).Scan(&status, &available, &onlineRequested)
	if errors.Is(err, pgx.ErrNoRows) && !*body.Available {
		ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"status": "offline", "available": false, "onlineRequested": false}})
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "availability_unavailable", "Availability could not be confirmed")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"status": status, "available": available, "onlineRequested": onlineRequested}})
}

func (a *driverAPI) driverSocketPresent(ctx *gin.Context) (bool, error) {
	var present bool
	if err := a.pool.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM driver_socket_sessions WHERE driver_id=$1::UUID AND expires_at>NOW())`, driverID(ctx)).Scan(&present); err != nil {
		return false, err
	}
	return present, nil
}

func (a *driverAPI) events(ctx *gin.Context) {
	events, err := messaging.NewEventStore(a.pool).ReadAfter(ctx.Request.Context(), driverID(ctx), ctx.Query("afterEventId"), 100)
	if err != nil {
		if errors.Is(err, messaging.ErrInvalidEventCursor) {
			driverError(ctx, http.StatusBadRequest, "invalid_event_cursor", "Event cursor is invalid")
		} else {
			driverError(ctx, http.StatusServiceUnavailable, "events_unavailable", "Events are unavailable")
		}
		return
	}
	var next *string
	if len(events) == 100 {
		id := events[len(events)-1].ID
		next = &id
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: events, Meta: contracts.APIMeta{NextCursor: next}})
}
