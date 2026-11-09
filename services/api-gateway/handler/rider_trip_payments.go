package handler

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/monnifysettings"
)

func registerRiderTripPayments(group *gin.RouterGroup, pool *pgxpool.Pool) {
	r := group.Group("/rider", gatewayauth.RequireRole("rider"))
	r.GET("/payments/config", func(c *gin.Context) {
		s, e := monnifysettings.Read(c.Request.Context(), pool, 0)
		if e != nil {
			heroFailure(c)
			return
		}
		heroData(c, gin.H{"directPaymentEnabled": true, "monnifyEnabled": s.Enabled && s.TripPaymentsEnabled && s.Configured(), "environment": s.Environment, "savedCardsSupported": false, "collectionTiming": "after_completion"})
	})
	r.GET("/trips/:tripID/payment", func(c *gin.Context) { riderPaymentStatus(c, pool) })
	r.POST("/trips/:tripID/payment/checkout", func(c *gin.Context) { riderPaymentAction(c, pool, false) })
	r.POST("/trips/:tripID/payment/refresh", func(c *gin.Context) { riderPaymentAction(c, pool, true) })
}
func riderPaymentAction(c *gin.Context, pool *pgxpool.Pool, refresh bool) {
	id := c.Param("tripID")
	if uuid.Validate(id) != nil {
		driverError(c, 400, "invalid_trip_id", "A valid trip ID is required.")
		return
	}
	u, _ := gatewayauth.CurrentUser(c)
	var owned bool
	if pool.QueryRow(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM trips WHERE id=$1::uuid AND rider_id=$2::uuid)`, id, u.ID).Scan(&owned) != nil {
		heroFailure(c)
		return
	}
	if !owned {
		driverError(c, 404, "trip_not_found", "Trip was not found.")
		return
	}
	if !refresh {
		var b struct {
			Confirmed bool `json:"confirmed"`
		}
		if c.ShouldBindJSON(&b) != nil || !b.Confirmed {
			driverError(c, 400, "confirmation_required", "Confirm payment through Monnify. Do not also pay the Driver directly.")
			return
		}
	}
	path := "/internal/trip-payments/checkout"
	if refresh {
		path = "/internal/trip-payments/refresh"
	}
	var result struct {
		Data map[string]any `json:"data"`
	}
	if e := paymentInternalRequest(c.Request.Context(), path, gin.H{"tripId": id, "riderId": u.ID}, &result); e != nil {
		var pe *paymentServiceError
		if errors.As(e, &pe) && (pe.Status == 409 || pe.Status == 404) {
			message := "Refresh this trip's payment status before trying again."
			switch pe.Code {
			case "verified_email_required":
				message = "Verify your account email before paying online."
			case "trip_not_completed":
				message = "Online payment is available after the trip is completed."
			case "payment_initializing":
				message = "Your checkout is being prepared. Wait a moment and check its status."
			case "payment_requires_review":
				message = "This payment needs review. Contact support before paying again."
			case "trip_collection_disabled":
				message = "Monnify trip checkout is currently unavailable."
			}
			driverError(c, pe.Status, pe.Code, message)
			return
		}
		driverError(c, 503, "trip_payment_unavailable", "Payment is temporarily unavailable. Check its status before paying again.")
		return
	}
	if refresh {
		riderPaymentStatus(c, pool)
		return
	}
	heroData(c, result.Data)
}
func riderPaymentStatus(c *gin.Context, pool *pgxpool.Pool) {
	id := c.Param("tripID")
	if uuid.Validate(id) != nil {
		driverError(c, 400, "invalid_trip_id", "A valid trip ID is required.")
		return
	}
	u, _ := gatewayauth.CurrentUser(c)
	var raw []byte
	e := pool.QueryRow(c.Request.Context(), `SELECT jsonb_build_object('tripId',t.id,'tripStatus',t.status,'collectionMethod',COALESCE(s.collection_method,'direct'),'settlementStatus',s.status,'amountKobo',s.expected_amount_kobo,'currency','NGN','paymentStatus',p.status,'checkoutUrl',CASE WHEN p.status='pending' THEN p.checkout_url ELSE NULL END,'providerVerified',COALESCE(p.status='success',false),'paymentReference',p.payment_reference,'environment',cfg.environment,'doNotPayDriverDirectly',COALESCE(s.collection_method='monnify',false)) FROM trips t LEFT JOIN trip_settlements s ON s.trip_id=t.id LEFT JOIN rider_trip_payments p ON p.trip_id=t.id LEFT JOIN admin_monnify_settings cfg ON cfg.version=p.monnify_config_version WHERE t.id=$1::uuid AND t.rider_id=$2::uuid`, id, u.ID).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		driverError(c, 404, "trip_not_found", "Trip was not found.")
		return
	}
	if e != nil {
		heroFailure(c)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(200, "application/json", append(append([]byte(`{"data":`), raw...), '}'))
}
