package handler

import (
	"context"
	"encoding/json"
	"errors"
	tripproto "github.com/luxipha/heyGo_backend/shared/proto/trip"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	grpcclient "github.com/luxipha/heyGo_backend/services/api-gateway/grpc-client"
	"github.com/luxipha/heyGo_backend/services/api-gateway/types"
	"github.com/luxipha/heyGo_backend/services/ride-agent-gateway/agent"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/driverstate"
	"github.com/luxipha/heyGo_backend/shared/httpmiddleware"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/messaging/pubsub"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	"github.com/luxipha/heyGo_backend/shared/observe/metrics"
	"github.com/luxipha/heyGo_backend/shared/storage"
	sharedtypes "github.com/luxipha/heyGo_backend/shared/types"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/otel"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// NewHTTPHandler initializes the HTTP handler with routes and middleware
func NewHTTPHandler(bus *pubsub.Client, connMgr *messaging.ConnectionManager, authMiddleware *gatewayauth.Middleware, users gatewayauth.UserStore, pool *pgxpool.Pool, files storage.ObjectStore, allowedOrigins []string, phoneAuth PhoneAuthConfig, readiness func(context.Context) error, eventStores ...*messaging.EventStore) *gin.Engine {
	r := gin.New()
	r.Use(stripCredentialQuery, gin.Recovery())
	eventStore := messaging.NewEventStore(pool)
	if len(eventStores) > 0 && eventStores[0] != nil {
		eventStore = eventStores[0]
	}

	middleware := otelgin.Middleware("api-gateway", otelgin.WithTracerProvider(otel.GetTracerProvider()))

	// Structured logging middleware
	r.Use(logs.HTTPLoggingMiddleware)
	r.Use(metrics.HTTPMiddleware("api-gateway"))
	r.Use(httpmiddleware.NewRateLimiter(120, time.Minute).Middleware)
	r.Use(corsMiddleware(allowedOrigins))

	r.GET("/health", healthHandler)
	r.GET("/metrics", gin.WrapH(metrics.Handler()))
	r.GET("/ready", func(ctx *gin.Context) {
		if err := readiness(ctx.Request.Context()); err != nil {
			ctx.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready", "error": err.Error()})
			return
		}
		ctx.JSON(http.StatusOK, gin.H{"status": "ready", "service": "api-gateway"})
	})
	if phoneUsers, ok := users.(*gatewayauth.PostgresUserStore); ok && pool != nil {
		registerPhoneAuthRoutes(r, pool, phoneUsers, phoneAuth)
	}
	registerAdminRoutes(r, pool, files)

	authenticated := r.Group("/", middleware, authMiddleware.Authenticate, idempotencyMiddleware(pool))
	authenticated.GET("/auth/me", func(ctx *gin.Context) { currentUserHandler(ctx, pool) })
	authenticated.POST("/auth/register", func(ctx *gin.Context) { registerRoleHandler(ctx, users) })
	if pool != nil {
		registerPhoneSessionRoutes(authenticated, pool)
		registerRiderIdentityRoutes(authenticated, pool, phoneAuth)
	}
	registerDeviceRoutes(authenticated, pool)
	registerDriverRoutes(authenticated, pool, files)
	registerSupportRoutes(authenticated, pool, files)
	registerNoShowRiderRoutes(authenticated, pool)
	registerRiderHomeRoutes(authenticated, pool)
	registerRideAgentRoutes(authenticated)
	registerRiderCommandRoutes(authenticated, pool, bus)
	registerRiderTripReadRoutes(authenticated, pool)
	registerRiderAccountRoutes(authenticated, pool, files)
	authenticated.POST("/trip/preview", gatewayauth.RequireRole("rider"), func(ctx *gin.Context) { previewTripHandler(ctx, pool) })
	authenticated.POST("/trip/start", gatewayauth.RequireRole("rider"), tripStartHandler)
	authenticated.POST("/trips/:tripID/complete", gatewayauth.RequireRole("driver"), func(ctx *gin.Context) { publishLifecycleCommand(ctx, bus, contracts.TripCmdComplete) })
	authenticated.POST("/trips/:tripID/arrival", gatewayauth.RequireRole("driver"), func(ctx *gin.Context) { publishLifecycleCommand(ctx, bus, contracts.TripCmdArrive) })
	authenticated.POST("/trips/:tripID/start", gatewayauth.RequireRole("driver"), func(ctx *gin.Context) { publishLifecycleCommand(ctx, bus, contracts.TripCmdStart) })
	authenticated.POST("/trips/:tripID/cancel", func(ctx *gin.Context) {
		user, _ := gatewayauth.CurrentUser(ctx)
		if pool != nil {
			if _, err := uuid.Parse(ctx.Param("tripID")); err != nil {
				driverError(ctx, 400, "invalid_trip_id", "Valid trip ID is required")
				return
			}
			var riderOwns bool
			if err := pool.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM trips WHERE id=$1::UUID AND rider_id=$2::UUID)`, ctx.Param("tripID"), user.ID).Scan(&riderOwns); err != nil {
				driverError(ctx, 503, "trip_unavailable", "Trip is unavailable")
				return
			}
			ctx.Set("riderCancellation", riderOwns)
		}
		publishLifecycleCommand(ctx, bus, contracts.TripCmdCancel)
	})
	authenticated.POST("/trips/:tripID/rating", func(ctx *gin.Context) { publishLifecycleCommand(ctx, bus, contracts.TripCmdRate) })
	registerTripChatRoutes(authenticated, pool)
	authenticated.GET("/ws/riders", gatewayauth.RequireRole("rider"), func(ctx *gin.Context) {
		ridersWSHandler(ctx, bus, connMgr, eventStore)
	})
	authenticated.GET("/ws/drivers", gatewayauth.RequireRole("driver"), func(ctx *gin.Context) {
		driversWSHandler(ctx, bus, connMgr, pool, eventStore)
	})

	return r
}

func publishLifecycleCommand(ctx *gin.Context, bus *pubsub.Client, command string) {
	user, _ := gatewayauth.CurrentUser(ctx)
	payload := messaging.TripLifecycleCommand{TripID: ctx.Param("tripID"), ActorID: user.ID}
	if _, err := uuid.Parse(payload.TripID); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_trip_id", "Valid trip ID is required")
		return
	}
	if command == contracts.TripCmdCancel {
		var body struct {
			Reason    string `json:"reason" binding:"required"`
			Confirmed bool   `json:"confirmed"`
		}
		if err := ctx.ShouldBindJSON(&body); err != nil {
			driverError(ctx, http.StatusBadRequest, "reason_required", "Reason is required")
			return
		}
		riderCancellation, _ := ctx.Get("riderCancellation")
		if (riderCancellation == true || (user.HasRole("rider") && !user.HasRole("driver"))) && !body.Confirmed {
			driverError(ctx, http.StatusBadRequest, "confirmation_required", "Confirm cancellation before submitting")
			return
		}
		payload.Reason = body.Reason
	}
	if command == contracts.TripCmdRate {
		var body struct {
			Rating       int      `json:"rating" binding:"required,min=1,max=5"`
			FeedbackTags []string `json:"feedbackTags"`
			Comment      string   `json:"comment"`
		}
		if err := ctx.ShouldBindJSON(&body); err != nil || body.Rating < 1 || body.Rating > 5 {
			driverError(ctx, http.StatusBadRequest, "invalid_rating", "Rating must be between 1 and 5")
			return
		}
		if riderRating, _ := ctx.Get("riderRating"); riderRating == true {
			if len(body.FeedbackTags) > 0 {
				driverError(ctx, http.StatusBadRequest, "invalid_feedback_tags", "Rider ratings do not accept driver feedback tags")
				return
			}
			comment, err := normalizeRiderRatingComment(body.Comment)
			if err != nil {
				driverError(ctx, http.StatusBadRequest, "invalid_rating_comment", err.Error())
				return
			}
			payload.Comment = comment
		} else {
			if strings.TrimSpace(body.Comment) != "" {
				driverError(ctx, http.StatusBadRequest, "rider_comment_only", "Written comments are only available for rider ratings")
				return
			}
			tags, err := normalizeDriverFeedbackTags(body.FeedbackTags)
			if err != nil {
				driverError(ctx, http.StatusBadRequest, "invalid_feedback_tags", err.Error())
				return
			}
			payload.FeedbackTags = tags
		}
		payload.Rating = body.Rating
	}
	data, err := json.Marshal(payload)
	if err != nil {
		driverError(ctx, http.StatusInternalServerError, "command_encoding_failed", "Failed to encode command")
		return
	}
	message := &contracts.EventMessage{EntityID: user.ID, Data: data}
	if key := strings.TrimSpace(ctx.GetHeader("Idempotency-Key")); key != "" {
		message.EventID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(user.ID+":"+key)).String()
	}
	if err := bus.Producer.SendMessage(ctx, command, message); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "command_unavailable", "Failed to queue trip command")
		return
	}
	ctx.JSON(http.StatusAccepted, contracts.APIResponse{Data: gin.H{"status": "accepted"}})
}

func currentUserHandler(ctx *gin.Context, pool *pgxpool.Pool) {
	user, _ := gatewayauth.CurrentUser(ctx)
	data := gin.H{"id": user.ID, "phone": user.Phone, "phoneVerified": user.PhoneVerified, "email": user.Email, "emailVerified": user.EmailVerified, "name": user.Name, "roles": user.Roles, "casperIDLinked": user.CasperSubject != "", "trustCapacity": gin.H{"provider": user.ProviderTrust, "consumer": user.ConsumerTrust}, "eligibleToDrive": false}
	if user.HasRole("rider") {
		data["riderDisplayName"] = user.RiderName
	}
	if user.HasRole("driver") {
		state, err := driverstate.Read(ctx.Request.Context(), pool, user.ID)
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "eligibility_unavailable", "Driver eligibility is unavailable")
			return
		}
		data["eligibleToDrive"] = state.EligibleToDrive
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: data})
}

func registerRoleHandler(ctx *gin.Context, users gatewayauth.UserStore) {
	var payload struct {
		Role string `json:"role" binding:"required"`
	}
	if err := ctx.ShouldBindJSON(&payload); err != nil {
		driverError(ctx, http.StatusBadRequest, "role_required", "Role is required")
		return
	}
	user, _ := gatewayauth.CurrentUser(ctx)
	registered, err := users.AddRole(ctx.Request.Context(), user.ID, payload.Role)
	if err != nil {
		if err == gatewayauth.ErrInvalidRole {
			driverError(ctx, http.StatusBadRequest, "invalid_role", err.Error())
			return
		}
		driverError(ctx, http.StatusInternalServerError, "registration_failed", "Failed to register role")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: registered})
}

// healthHandler handles liveness probe requests
func healthHandler(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, gin.H{"status": "ok", "service": "api-gateway"})
}

// tripStartHandler handles trip start requests
func tripStartHandler(ctx *gin.Context) {
	var payload types.TripStartRequest
	if err := ctx.ShouldBindJSON(&payload); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_request", "Invalid request")
		return
	}
	if _, err := uuid.Parse(payload.FareID); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_fare_id", "Valid rideFareID is required")
		return
	}
	if !payload.Confirmed {
		driverError(ctx, 400, "confirmation_required", "Explicit Rider confirmation is required")
		return
	}
	if ctx.GetHeader("Idempotency-Key") == "" {
		driverError(ctx, 400, "idempotency_key_required", "A stable request identity is required")
		return
	}

	tripService, err := grpcclient.NewTripServiceClient()
	if err != nil {
		logs.L().Errorw("trip service unavailable", "error", err)
		driverError(ctx, http.StatusServiceUnavailable, "trip_service_unavailable", "Trip service is unavailable")
		return
	}
	defer tripService.Close()

	user, _ := gatewayauth.CurrentUser(ctx)
	trip, err := tripService.Client.CreateTrip(ctx, payload.ToProto(user.ID))
	if err != nil {
		if publicTripError(ctx, err) {
			return
		}

		if status.Code(err) == codes.AlreadyExists {
			driverError(ctx, http.StatusConflict, "active_trip_exists", "Recover your current trip before requesting another")
			return
		}
		if status.Code(err) == codes.FailedPrecondition {
			driverError(ctx, http.StatusConflict, "trip_market_unavailable", "Pickup or destination cannot be classified under the current service map")
			return
		}
		driverError(ctx, http.StatusInternalServerError, "trip_creation_failed", "Failed to start trip")
		return
	}

	res := contracts.APIResponse{Data: trip}
	ctx.JSON(http.StatusOK, res)
}

// previewTripHandler handles trip preview requests
func previewTripHandler(ctx *gin.Context, pool *pgxpool.Pool) {
	var payload types.PreviewTripRequest
	if err := ctx.ShouldBindJSON(&payload); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_request", "Invalid request")
		return
	}
	if !validCoordinate(payload.Pickup.Latitude, payload.Pickup.Longitude) || !validCoordinate(payload.Destination.Latitude, payload.Destination.Longitude) || (payload.Pickup == payload.Destination) {
		driverError(ctx, http.StatusBadRequest, "invalid_route", "Valid, distinct pickup and destination coordinates are required")
		return
	}

	var pickupMeta, destinationMeta any
	if payload.SchemaVersion != 0 && payload.SchemaVersion != 1 {
		driverError(ctx, 400, "invalid_schema_version", "Unsupported ride schema")
		return
	}
	if payload.SchemaVersion == 1 {
		if payload.DestinationPlaceID == "" {
			driverError(ctx, 400, "destination_unconfirmed", "Resolve and confirm a destination before previewing")
			return
		}
		reply, err := callRideAgent(ctx.Request.Context(), agent.Request{SchemaVersion: 1, PlaceID: payload.DestinationPlaceID})
		if err != nil || reply.Place == nil {
			driverError(ctx, 503, "places_unavailable", "Destination resolution is unavailable")
			return
		}
		destinationMeta = reply.Place
		payload.Destination = sharedtypes.Coordinate{Latitude: reply.Place.Location.Latitude, Longitude: reply.Place.Location.Longitude}
		pickupMeta = map[string]any{"provider": "device", "name": "Current location", "location": payload.Pickup}
		if payload.SourceTripID != "" && payload.PickupPlaceID == "" {
			if _, err := uuid.Parse(payload.SourceTripID); err != nil {
				driverError(ctx, 400, "invalid_trip_id", "Valid previous trip ID is required")
				return
			}
			previous, err := (&riderHomeAPI{pool: pool}).readTrip(ctx, payload.SourceTripID, false)
			if err != nil {
				driverError(ctx, 503, "trip_unavailable", "Previous trip is unavailable")
				return
			}
			if previous == nil {
				driverError(ctx, 404, "trip_not_found", "Previous trip was not found")
				return
			}
			if previous.Status != "completed" && previous.Status != "cancelled" {
				driverError(ctx, 409, "trip_not_terminal", "Only an ended trip may be rebooked")
				return
			}
			var point sharedtypes.Coordinate
			if json.Unmarshal(previous.Pickup, &point) != nil || string(previous.Pickup) == "null" || !validCoordinate(point.Latitude, point.Longitude) {
				driverError(ctx, 409, "pickup_unavailable", "Choose a pickup location")
				return
			}
			payload.Pickup = point
			pickupMeta = map[string]any{"provider": "trip_history", "name": "Previous pickup", "sourceTripId": previous.ID, "location": point}
		}
		if payload.PickupPlaceID != "" {
			reply, err := callRideAgent(ctx.Request.Context(), agent.Request{SchemaVersion: 1, PlaceID: payload.PickupPlaceID})
			if err != nil || reply.Place == nil {
				driverError(ctx, 503, "places_unavailable", "Pickup resolution is unavailable")
				return
			}
			pickupMeta = reply.Place
			payload.Pickup = sharedtypes.Coordinate{Latitude: reply.Place.Location.Latitude, Longitude: reply.Place.Location.Longitude}
		}
		if payload.Pickup == payload.Destination {
			driverError(ctx, 400, "invalid_route", "Pickup and destination must differ")
			return
		}
	}

	tripService, err := grpcclient.NewTripServiceClient()
	if err != nil {
		logs.L().Errorw("trip service unavailable", "error", err)
		driverError(ctx, http.StatusServiceUnavailable, "trip_service_unavailable", "Trip service is unavailable")
		return
	}
	defer tripService.Close()

	user, _ := gatewayauth.CurrentUser(ctx)
	tripPreview, err := tripService.Client.PreviewTrip(ctx, payload.ToProto(user.ID))
	if err != nil {
		if !publicTripError(ctx, err) {
			driverError(ctx, 503, "route_unavailable", "A route or approved market pricing is unavailable")
		}
		return
	}

	encoded, err := json.Marshal(tripPreview)
	if err != nil {
		driverError(ctx, http.StatusInternalServerError, "trip_preview_failed", "Failed to encode trip preview")
		return
	}
	var data map[string]any
	if err := json.Unmarshal(encoded, &data); err != nil {
		driverError(ctx, http.StatusInternalServerError, "trip_preview_failed", "Failed to encode trip preview")
		return
	}
	if payload.SchemaVersion == 1 {
		normalized, err := publicRiderRoute(tripPreview.GetRoute())
		if err != nil {
			driverError(ctx, 503, "route_unavailable", "A route is unavailable")
			return
		}
		data["route"] = normalized
	}
	fares, _ := data["rideFares"].([]any)
	for _, rawFare := range fares {
		fare, ok := rawFare.(map[string]any)
		if !ok {
			continue
		}
		fareID, _ := fare["id"].(string)
		var fareAmount, debt int64
		var expiresAt time.Time
		var previewID string
		if err := pool.QueryRow(ctx.Request.Context(), `SELECT ROUND(total_fare_minor)::BIGINT,no_show_debt_kobo,expires_at,preview_id::TEXT FROM ride_fares WHERE id=$1::UUID AND rider_id=$2::UUID AND expires_at>NOW()`, fareID, user.ID).Scan(&fareAmount, &debt, &expiresAt, &previewID); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "Rider amount due is unavailable")
			return
		}
		if payload.SchemaVersion == 1 {
			delete(fare, "totalFareInPaise")
		}
		fare["fareKobo"] = fareAmount
		fare["currency"] = "NGN"
		fare["expiresAt"] = expiresAt
		fare["previewId"] = previewID
		fare["availability"] = map[string]any{"status": "not_checked", "pickupEtaSeconds": nil}
		data["previewId"] = previewID
		data["currency"] = "NGN"
		fare["noShowDebtKobo"] = debt
		fare["amountDueKobo"] = fareAmount + debt
	}
	if payload.SchemaVersion == 1 {
		pickupJSON, _ := json.Marshal(pickupMeta)
		destinationJSON, _ := json.Marshal(destinationMeta)
		if _, err := pool.Exec(ctx.Request.Context(), `UPDATE ride_fares SET pickup_metadata=$3::JSONB,destination_metadata=$4::JSONB WHERE rider_id=$1::UUID AND preview_id=$2::UUID`, user.ID, data["previewId"], pickupJSON, destinationJSON); err != nil {
			driverError(ctx, 503, "preview_unavailable", "Preview metadata is unavailable")
			return
		}
		data["pickup"] = pickupMeta
		data["destination"] = destinationMeta
		data["schemaVersion"] = 1
	}

	res := contracts.APIResponse{Data: data}
	ctx.JSON(http.StatusOK, res)
}

func validCoordinate(latitude, longitude float64) bool {
	return latitude >= -90 && latitude <= 90 && longitude >= -180 && longitude <= 180
}

// enableCORS is a middleware to handle CORS requests
func corsMiddleware(allowedOrigins []string) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		origin := strings.TrimSuffix(ctx.GetHeader("Origin"), "/")
		if strings.HasPrefix(ctx.Request.URL.Path, "/admin/") && origin != "" && !slices.Contains(allowedOrigins, origin) {
			ctx.AbortWithStatus(http.StatusForbidden)
			return
		}
		if origin != "" && slices.Contains(allowedOrigins, origin) {
			ctx.Header("Access-Control-Allow-Origin", origin)
			ctx.Header("Vary", "Origin")
			ctx.Header("Access-Control-Allow-Credentials", "true")
			ctx.Header("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
			ctx.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, Idempotency-Key, X-CSRF-Token")
		}
		if ctx.Request.Method == http.MethodOptions {
			if origin == "" || !slices.Contains(allowedOrigins, origin) {
				ctx.AbortWithStatus(http.StatusForbidden)
				return
			}
			ctx.AbortWithStatus(http.StatusNoContent)
			return
		}
		ctx.Next()
	}
}

func publicTripError(ctx *gin.Context, err error) bool {
	for _, detail := range status.Convert(err).Details() {
		info, ok := detail.(*errdetails.ErrorInfo)
		if !ok || info.Domain != "heygo.rider" {
			continue
		}
		code := 503
		switch info.Reason {
		case "fare_invalid":
			code = 404
		case "fare_expired":
			code = 410
		case "fare_foreign", "rider_restricted":
			code = 403
		case "debt_changed":
			code = 409
		case "market_unavailable":
			code = 409
		}
		message := "Ride requests are temporarily unavailable."
		switch info.Reason {
		case "fare_invalid":
			message = "This ride option is invalid. Refresh your ride options."
		case "fare_expired":
			message = "This ride preview expired. Refresh your ride options."
		case "fare_foreign":
			message = "This ride option belongs to another account."
		case "rider_restricted":
			message = "Ride requests are currently restricted for your account."
		case "market_unavailable":
			message = "Rides are unavailable for this route."
		case "debt_changed":
			message = "Your amount due changed. Review a new preview."
		}
		driverError(ctx, code, info.Reason, message)
		return true
	}
	return false
}

// Version 1 uses a GeoJSON LineString; legacy protobuf geometry is unchanged.
func publicRiderRoute(route *tripproto.Route) (map[string]any, error) {
	if route == nil || !finitePositive(route.Distance) || !finitePositive(route.Duration) || len(route.Geometry) != 1 || route.Geometry[0] == nil {
		return nil, errors.New("invalid route")
	}
	points := route.Geometry[0].Coordinates
	if len(points) < 2 {
		return nil, errors.New("invalid route geometry")
	}
	coordinates := make([][]float64, 0, len(points))
	for _, point := range points {
		if point == nil || !validCoordinate(point.Latitude, point.Longitude) {
			return nil, errors.New("invalid route coordinate")
		}
		coordinates = append(coordinates, []float64{point.Longitude, point.Latitude})
	}
	return map[string]any{"distance": route.Distance, "duration": route.Duration, "geometry": map[string]any{"type": "LineString", "coordinates": coordinates}}, nil
}
func finitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
