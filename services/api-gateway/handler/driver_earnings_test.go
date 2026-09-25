package handler

import (
	"testing"
	"time"
)

func TestEarningDayBoundsUseConfiguredCalendarDay(t *testing.T) {
	location, err := time.LoadLocation(driverEarningsTimeZone)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 23, 30, 0, 0, time.UTC)
	start, end, date := earningDayBounds(now, location)
	if date != "2026-09-25" || !start.Equal(time.Date(2026, 9, 24, 23, 0, 0, 0, time.UTC)) || !end.Equal(time.Date(2026, 9, 25, 23, 0, 0, 0, time.UTC)) {
		t.Fatalf("date=%s start=%s end=%s", date, start, end)
	}
}

func TestEarningsWindowsAlignInLagosTime(t *testing.T) {
	location, err := time.LoadLocation(driverEarningsTimeZone)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 23, 30, 0, 0, time.UTC)
	week, err := earningsWindowFor("week", now, location)
	if err != nil {
		t.Fatal(err)
	}
	if week.BucketCount != 7 || week.CurrentStart.In(location).Format("2006-01-02 15:04") != "2026-09-19 00:00" || week.PriorEnd.In(location).Format("2006-01-02 15:04") != "2026-09-18 00:30" {
		t.Fatalf("unexpected week window: %+v; prior end %s", week, week.PriorEnd.In(location))
	}
	day, err := earningsWindowFor("day", now, location)
	if err != nil {
		t.Fatal(err)
	}
	if day.BucketCount != 24 || day.CurrentStart.In(location).Format("2006-01-02") != "2026-09-25" || day.PriorEnd.In(location).Format("2006-01-02 15:04") != "2026-09-24 00:30" {
		t.Fatalf("unexpected day window: %+v; prior end %s", day, day.PriorEnd.In(location))
	}
}

func TestEarningsMonthComparisonClampsAtPreviousMonthEnd(t *testing.T) {
	location, err := time.LoadLocation(driverEarningsTimeZone)
	if err != nil {
		t.Fatal(err)
	}
	window, err := earningsWindowFor("month", time.Date(2026, 3, 31, 12, 0, 0, 0, location), location)
	if err != nil {
		t.Fatal(err)
	}
	if window.BucketCount != 31 || window.PriorEnd.In(location).Format("2006-01-02 15:04") != "2026-02-28 12:00" {
		t.Fatalf("unexpected month comparison window: %+v; prior end %s", window, window.PriorEnd.In(location))
	}
	series := emptyEarningsSeries(window, true)
	if len(series) != 31 || series[27].BucketFrom == nil || series[28].BucketFrom != nil || series[30].Label != "31" {
		t.Fatalf("comparison series should pad dates past February with empty buckets: %+v", series[27:])
	}
}

func TestAggregateDriverEarningsReturnsCurrentAndComparisonSeries(t *testing.T) {
	location, err := time.LoadLocation(driverEarningsTimeZone)
	if err != nil {
		t.Fatal(err)
	}
	window, err := earningsWindowFor("week", time.Date(2026, 9, 24, 23, 30, 0, 0, time.UTC), location)
	if err != nil {
		t.Fatal(err)
	}
	result := driverEarningsResponse{Period: window.Period, Series: emptyEarningsSeries(window, false), ComparisonSeries: emptyEarningsSeries(window, true)}
	records := []driverEarningRecord{
		{At: window.CurrentStart.AddDate(0, 0, 1).Add(time.Hour), FareKobo: 600000, EarningsKobo: 600000},
		{At: window.PriorStart.AddDate(0, 0, 1).Add(time.Hour), FareKobo: 400000, CommissionKobo: 10000, EarningsKobo: 390000},
		{At: window.CurrentEnd.Add(time.Hour), FareKobo: 900000, EarningsKobo: 900000},
	}
	got := aggregateDriverEarnings(result, window, records)
	if got.Totals.FareKobo != 600000 || got.Totals.AmountKobo != 600000 || got.Totals.CompletedTrips != 1 || got.Totals.BonusKobo != 0 || got.Totals.AveragePerDayKobo != 85714 {
		t.Fatalf("current totals: %+v", got.Totals)
	}
	if got.Comparison.FareKobo != 400000 || got.Comparison.AmountKobo != 390000 || got.Comparison.CommissionKobo != 10000 || got.Comparison.CompletedTrips != 1 {
		t.Fatalf("comparison totals: %+v", got.Comparison)
	}
	if got.ChangePercent == nil || *got.ChangePercent != ((600000.0-390000.0)/390000.0)*100 {
		t.Fatalf("changePercent=%v", got.ChangePercent)
	}
	if len(got.Series) != 7 || got.Series[1].AmountKobo != 600000 || len(got.ComparisonSeries) != 7 || got.ComparisonSeries[1].AmountKobo != 390000 {
		t.Fatalf("unexpected chart series current=%+v comparison=%+v", got.Series, got.ComparisonSeries)
	}
}
