package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/contracts"
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

type driverEarningsTotals struct {
	FareKobo          int64 `json:"fareKobo"`
	CommissionKobo    int64 `json:"commissionKobo"`
	AmountKobo        int64 `json:"earningsKobo"`
	BonusKobo         int64 `json:"bonusKobo"`
	CompletedTrips    int64 `json:"completedTrips"`
	AveragePerDayKobo int64 `json:"averagePerDayKobo"`
}

type driverEarningsPoint struct {
	Label          string     `json:"label"`
	BucketFrom     *time.Time `json:"bucketFrom,omitempty"`
	FareKobo       int64      `json:"fareKobo"`
	CommissionKobo int64      `json:"commissionKobo"`
	AmountKobo     int64      `json:"earningsKobo"`
	BonusKobo      int64      `json:"bonusKobo"`
	Trips          int64      `json:"completedTrips"`
}

type earningsWindow struct {
	Period       string
	Bucket       string
	CurrentStart time.Time
	CurrentEnd   time.Time
	PriorStart   time.Time
	PriorEnd     time.Time
	BucketCount  int
	Location     *time.Location
}

type driverEarningRecord struct {
	At             time.Time
	FareKobo       int64
	CommissionKobo int64
	EarningsKobo   int64
}

type driverEarningsResponse struct {
	Period           string                `json:"period"`
	TimeZone         string                `json:"timeZone"`
	Currency         string                `json:"currency"`
	From             time.Time             `json:"from"`
	To               time.Time             `json:"to"`
	Totals           driverEarningsTotals  `json:"totals"`
	Comparison       driverEarningsTotals  `json:"comparison"`
	ComparisonFrom   time.Time             `json:"comparisonFrom"`
	ComparisonTo     time.Time             `json:"comparisonTo"`
	ChangePercent    *float64              `json:"changePercent"`
	Series           []driverEarningsPoint `json:"series"`
	ComparisonSeries []driverEarningsPoint `json:"comparisonSeries"`
}

func earningsWindowFor(period string, now time.Time, location *time.Location) (earningsWindow, error) {
	local := now.In(location)
	dayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	w := earningsWindow{Period: period, Location: location}
	switch period {
	case "day":
		w.Bucket = "hour"
		w.BucketCount = 24
		w.CurrentStart = dayStart
		w.CurrentEnd = local
		w.PriorStart = dayStart.AddDate(0, 0, -1)
		w.PriorEnd = local.AddDate(0, 0, -1)
	case "week":
		w.Bucket = "day"
		w.BucketCount = 7
		w.CurrentStart = dayStart.AddDate(0, 0, -6)
		w.CurrentEnd = local
		w.PriorStart = w.CurrentStart.AddDate(0, 0, -7)
		w.PriorEnd = local.AddDate(0, 0, -7)
	case "month":
		w.Bucket = "day"
		w.CurrentStart = time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, location)
		w.CurrentEnd = local
		w.BucketCount = local.Day()
		w.PriorStart = w.CurrentStart.AddDate(0, -1, 0)
		priorMonthLast := w.PriorStart.AddDate(0, 1, -1).Day()
		priorDay := local.Day()
		if priorDay > priorMonthLast {
			priorDay = priorMonthLast
		}
		w.PriorEnd = time.Date(w.PriorStart.Year(), w.PriorStart.Month(), priorDay, local.Hour(), local.Minute(), local.Second(), local.Nanosecond(), location)
	default:
		return earningsWindow{}, fmt.Errorf("period must be day, week, or month")
	}
	return w, nil
}

func (w earningsWindow) bucketIndex(at time.Time, prior bool) (int, bool) {
	start, end := w.CurrentStart, w.CurrentEnd
	if prior {
		start, end = w.PriorStart, w.PriorEnd
	}
	local := at.In(w.Location)
	if local.Before(start) || !local.Before(end) {
		return 0, false
	}
	if w.Bucket == "hour" {
		return local.Hour(), true
	}
	return int(time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, w.Location).Sub(start) / (24 * time.Hour)), true
}

func earningsPointLabel(w earningsWindow, index int, prior bool) (string, *time.Time) {
	start := w.CurrentStart
	if prior {
		start = w.PriorStart
	}
	var bucket time.Time
	if w.Bucket == "hour" {
		bucket = time.Date(start.Year(), start.Month(), start.Day(), index, 0, 0, 0, w.Location)
		return bucket.Format("15:00"), timePointer(bucket.UTC())
	}
	if w.Period == "month" {
		monthEnd := start.AddDate(0, 1, 0)
		if start.AddDate(0, 0, index).Before(monthEnd) {
			bucket = start.AddDate(0, 0, index)
			return fmt.Sprintf("%d", index+1), timePointer(bucket.UTC())
		}
		return fmt.Sprintf("%d", index+1), nil
	}
	bucket = start.AddDate(0, 0, index)
	return bucket.Format("Mon"), timePointer(bucket.UTC())
}

func emptyEarningsSeries(w earningsWindow, prior bool) []driverEarningsPoint {
	points := make([]driverEarningsPoint, w.BucketCount)
	for i := range points {
		label, from := earningsPointLabel(w, i, prior)
		points[i] = driverEarningsPoint{Label: label, BucketFrom: from}
	}
	return points
}

func (a *driverAPI) earnings(ctx *gin.Context) {
	period := strings.TrimSpace(ctx.DefaultQuery("period", "week"))
	location, err := time.LoadLocation(driverEarningsTimeZone)
	if err != nil {
		driverError(ctx, http.StatusInternalServerError, "earnings_timezone_unavailable", "Earnings timezone is unavailable")
		return
	}
	window, err := earningsWindowFor(period, time.Now(), location)
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_earnings_period", err.Error())
		return
	}
	result, err := readDriverEarnings(ctx.Request.Context(), a.pool, driverID(ctx), window)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "earnings_unavailable", "Earnings are unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: result})
}

func readDriverEarnings(ctx context.Context, pool *pgxpool.Pool, driver string, w earningsWindow) (driverEarningsResponse, error) {
	result := driverEarningsResponse{Period: w.Period, TimeZone: driverEarningsTimeZone, Currency: "NGN", From: w.CurrentStart.UTC(), To: w.CurrentEnd.UTC(), ComparisonFrom: w.PriorStart.UTC(), ComparisonTo: w.PriorEnd.UTC(), Series: emptyEarningsSeries(w, false), ComparisonSeries: emptyEarningsSeries(w, true)}
	start := w.CurrentStart
	if w.PriorStart.Before(start) {
		start = w.PriorStart
	}
	end := w.CurrentEnd
	if w.PriorEnd.After(end) {
		end = w.PriorEnd
	}
	rows, err := pool.Query(ctx, `SELECT accrued_at,fare_kobo,commission_kobo,earnings_kobo FROM driver_trip_earnings
		WHERE driver_id=$1::UUID AND accrued_at>=$2 AND accrued_at<$3 ORDER BY accrued_at`, driver, start.UTC(), end.UTC())
	if err != nil {
		return driverEarningsResponse{}, fmt.Errorf("query driver earnings: %w", err)
	}
	defer rows.Close()
	records := make([]driverEarningRecord, 0)
	for rows.Next() {
		var record driverEarningRecord
		if err := rows.Scan(&record.At, &record.FareKobo, &record.CommissionKobo, &record.EarningsKobo); err != nil {
			return driverEarningsResponse{}, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return driverEarningsResponse{}, err
	}
	return aggregateDriverEarnings(result, w, records), nil
}

func aggregateDriverEarnings(result driverEarningsResponse, w earningsWindow, records []driverEarningRecord) driverEarningsResponse {
	for _, record := range records {
		if index, ok := w.bucketIndex(record.At, false); ok {
			addEarnings(&result.Totals, &result.Series[index], record.FareKobo, record.CommissionKobo, record.EarningsKobo)
		}
		if index, ok := w.bucketIndex(record.At, true); ok {
			addEarnings(&result.Comparison, &result.ComparisonSeries[index], record.FareKobo, record.CommissionKobo, record.EarningsKobo)
		}
	}
	currentDays := int(w.CurrentEnd.Sub(w.CurrentStart)/(24*time.Hour)) + 1
	if w.Period == "day" {
		currentDays = 1
	}
	if w.Period == "week" {
		currentDays = 7
	}
	if w.Period == "month" {
		currentDays = w.CurrentEnd.In(w.Location).Day()
	}
	if currentDays > 0 {
		result.Totals.AveragePerDayKobo = result.Totals.AmountKobo / int64(currentDays)
	}
	if result.Comparison.AmountKobo > 0 {
		change := float64(result.Totals.AmountKobo-result.Comparison.AmountKobo) * 100 / float64(result.Comparison.AmountKobo)
		result.ChangePercent = &change
	}
	return result
}

func addEarnings(totals *driverEarningsTotals, point *driverEarningsPoint, fare, commission, earnings int64) {
	totals.FareKobo += fare
	totals.CommissionKobo += commission
	totals.AmountKobo += earnings
	totals.CompletedTrips++
	point.FareKobo += fare
	point.CommissionKobo += commission
	point.AmountKobo += earnings
	point.Trips++
}

func timePointer(value time.Time) *time.Time { return &value }
