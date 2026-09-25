package handler

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type todayEarnings struct {
	Date           string `json:"date"`
	TimeZone       string `json:"timeZone"`
	Currency       string `json:"currency"`
	FareKobo       int64  `json:"fareKobo"`
	CommissionKobo int64  `json:"commissionKobo"`
	AmountKobo     int64  `json:"amountKobo"`
	CompletedTrips int64  `json:"completedTrips"`
}

const driverEarningsTimeZone = "Africa/Lagos"

func earningDayBounds(now time.Time, location *time.Location) (time.Time, time.Time, string) {
	local := now.In(location)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	return start.UTC(), start.AddDate(0, 0, 1).UTC(), start.Format("2006-01-02")
}

func readTodayEarnings(ctx context.Context, pool *pgxpool.Pool, driverID string, now time.Time) (todayEarnings, error) {
	location, err := time.LoadLocation(driverEarningsTimeZone)
	if err != nil {
		return todayEarnings{}, fmt.Errorf("invalid earnings timezone: %w", err)
	}
	start, end, date := earningDayBounds(now, location)
	result := todayEarnings{Date: date, TimeZone: driverEarningsTimeZone, Currency: "NGN"}
	err = pool.QueryRow(ctx, `SELECT COALESCE(SUM(fare_kobo),0)::BIGINT,COALESCE(SUM(commission_kobo),0)::BIGINT,COALESCE(SUM(earnings_kobo),0)::BIGINT,COUNT(*)
  FROM driver_trip_earnings WHERE driver_id=$1::UUID AND accrued_at>=$2 AND accrued_at<$3`, driverID, start, end).Scan(&result.FareKobo, &result.CommissionKobo, &result.AmountKobo, &result.CompletedTrips)
	if err != nil {
		return todayEarnings{}, fmt.Errorf("read today's driver earnings: %w", err)
	}
	return result, nil
}
