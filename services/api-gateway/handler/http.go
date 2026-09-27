package handler

import (
	"context"
	"encoding/json"
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
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/driverstate"
	"github.com/luxipha/heyGo_backend/shared/httpmiddleware"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/messaging/pubsub"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	"github.com/luxipha/heyGo_backend/shared/observe/metrics"
	"github.com/luxipha/heyGo_backend/shared/storage"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.opentelemetry.io/otel"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// NewHTTPHandler initializes the HTTP handler with routes and middleware
func NewHTTPHandler(bus *pubsub.Client, connMgr *messaging.ConnectionManager, authMiddleware *gatewayauth.Middleware, users gatewayauth.UserStore, pool *pgxpool.Pool, files storage.ObjectStore, allowedOrigins []string, oauth CasperIDOAuthConfig, readiness func(context.Context) error, eventStores ...*messaging.EventStore) *gin.Engine {
	r := gin.Default()
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
	r.POST("/auth/casperid/exchange", casperIDExchangeHandler(oauth))
	registerAdminRoutes(r, pool, files)

	authenticated := r.Group("/", middleware, authMiddleware.Authenticate, idempotencyMiddleware(pool))
	authenticated.GET("/auth/me", func(ctx *gin.Context) { currentUserHandler(ctx, pool) })
	authenticated.POST("/auth/register", func(ctx *gin.Context) { registerRoleHandler(ctx, users) })
	registerDeviceRoutes(authenticated, pool)
	registerDriverRoutes(authenticated, pool, files)
	registerSupportRoutes(authenticated, pool, files)
	registerNoShowRiderRoutes(authenticated, pool)
	riderRatings := authenticated.Group("/rider", gatewayauth.RequireRole("rider"))
	riderRatings.POST("/trips/:tripID/rating", func(ctx *gin.Context) {
		ctx.Set("riderRating", true)
		publishLifecycleCommand(ctx, bus, contracts.TripCmdRate)
	})
	authenticated.POST("/trip/preview", gatewayauth.RequireRole("rider"), func(ctx *gin.Context) { previewTripHandler(ctx, pool) })
	authenticated.POST("/trip/start", gatewayauth.RequireRole("rider"), tripStartHandler)
	authenticated.POST("/trips/:tripID/complete", gatewayauth.RequireRole("driver"), func(ctx *gin.Context) { publishLifecycleCommand(ctx, bus, contracts.TripCmdComplete) })
	authenticated.POST("/trips/:tripID/arrival", gatewayauth.RequireRole("driver"), func(ctx *gin.Context) { publishLifecycleCommand(ctx, bus, contracts.TripCmdArrive) })
	authenticated.POST("/trips/:tripID/start", gatewayauth.RequireRole("driver"), func(ctx *gin.Context) { publishLifecycleCommand(ctx, bus, contracts.TripCmdStart) })
	authenticated.POST("/trips/:tripID/cancel", func(ctx *gin.Context) { publishLifecycleCommand(ctx, bus, contracts.TripCmdCancel) })
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
			Reason string `json:"reason" binding:"required"`
		}
		if err := ctx.ShouldBindJSON(&body); err != nil {
			driverError(ctx, http.StatusBadRequest, "reason_required", "Reason is required")
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
	data := gin.H{"id": user.ID, "humanId": user.HumanID, "roles": user.Roles, "verified": user.Verified, "kycTier": user.KYCTier, "trustScore": user.TrustScore, "eligibleToDrive": false}
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
		driverError(ctx, http.StatusInternalServerError, "trip_preview_failed", "Failed to preview trip")
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
	fares, _ := data["rideFares"].([]any)
	for _, rawFare := range fares {
		fare, ok := rawFare.(map[string]any)
		if !ok {
			continue
		}
		fareID, _ := fare["id"].(string)
		var fareAmount, debt int64
		if err := pool.QueryRow(ctx.Request.Context(), `SELECT ROUND(total_fare_minor)::BIGINT,no_show_debt_kobo FROM ride_fares WHERE id=$1::UUID AND rider_id=$2::UUID AND expires_at>NOW()`, fareID, user.ID).Scan(&fareAmount, &debt); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "no_show_unavailable", "Rider amount due is unavailable")
			return
		}
		fare["noShowDebtKobo"] = debt
		fare["amountDueKobo"] = fareAmount + debt
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
