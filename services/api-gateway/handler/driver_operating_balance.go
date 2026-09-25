package handler

import (
	"context"
	"net/http"

	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

func readDriverOperatingBalance(ctx context.Context, pool *pgxpool.Pool, id string) (gin.H, error) {
	var balance int64
	var market *string
	var policyCount int
	var minimum, warning int64
	var topupMinimum, topupMaximum *int64
	var locationFresh bool
	err := pool.QueryRow(ctx, `SELECT COALESCE((SELECT balance_kobo FROM driver_operating_accounts WHERE driver_id=$1::UUID),0),
		(SELECT driver_market_at(location) FROM driver_live_locations WHERE driver_id=$1::UUID),
		COALESCE((SELECT recorded_at>=NOW()-INTERVAL '30 minutes' FROM driver_live_locations WHERE driver_id=$1::UUID),FALSE),
		(SELECT COUNT(*) FROM operating_balance_policies WHERE market_code=(SELECT driver_market_at(location) FROM driver_live_locations WHERE driver_id=$1::UUID)
		 AND status='approved' AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW())),
		COALESCE((SELECT MIN(minimum_kobo) FROM operating_balance_policies WHERE market_code=(SELECT driver_market_at(location) FROM driver_live_locations WHERE driver_id=$1::UUID)
		 AND status='approved' AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW())),0),
		COALESCE((SELECT MIN(warning_kobo) FROM operating_balance_policies WHERE market_code=(SELECT driver_market_at(location) FROM driver_live_locations WHERE driver_id=$1::UUID)
		 AND status='approved' AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW())),0),
		(SELECT MIN(topup_minimum_kobo) FROM operating_balance_policies WHERE market_code=(SELECT driver_market_at(location) FROM driver_live_locations WHERE driver_id=$1::UUID)
		 AND status='approved' AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW())),
		(SELECT MIN(topup_maximum_kobo) FROM operating_balance_policies WHERE market_code=(SELECT driver_market_at(location) FROM driver_live_locations WHERE driver_id=$1::UUID)
		 AND status='approved' AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW()))`, id).Scan(&balance, &market, &locationFresh, &policyCount, &minimum, &warning, &topupMinimum, &topupMaximum)
	if err != nil {
		return nil, err
	}
	var minimumValue, warningValue any
	if policyCount == 1 {
		minimumValue, warningValue = minimum, warning
	}
	return gin.H{"currency": "NGN", "balanceKobo": balance, "marketCode": market,
		"minimumKobo": minimumValue, "warningKobo": warningValue,
		"topupMinimumKobo": topupMinimum, "topupMaximumKobo": topupMaximum,
		"locationFresh": locationFresh, "canGoOnline": locationFresh && policyCount == 1 && balance >= minimum,
		"lowBalance": policyCount == 1 && balance < minimum}, nil
}

func (a *driverAPI) operatingBalance(ctx *gin.Context) {
	result, err := readDriverOperatingBalance(ctx.Request.Context(), a.pool, driverID(ctx))
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "operating_balance_unavailable", "Operating Balance is unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: result})
}
