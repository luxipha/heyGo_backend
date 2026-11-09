package handler

import (
	"context"
	"github.com/jackc/pgx/v5"
	"time"
)

// The caller holds the Rider and booking locks and has validated the fare.
// No money/ledger rows are touched. Exclusion constraints protect commitments;
// a conflict rolls back all changes, including the preview's consumed marker.
func applyScheduledEdit(ctx context.Context, tx pgx.Tx, id, oldFare string, driver *string, version int64, b scheduledEditInput, p schedulePolicy, region, destRegion string, seconds float64) error {
	if _, err := tx.Exec(ctx, `UPDATE ride_fares f SET guest_name=old.guest_name,guest_phone=old.guest_phone,ride_preferences=old.ride_preferences FROM ride_fares old WHERE f.id=$1::uuid AND old.id=$2::uuid`, b.FareID, oldFare); err != nil {
		return err
	}
	_, e := tx.Exec(ctx, `UPDATE scheduled_rides SET fare_id=$2::uuid,policy_version=$3,pickup_at=$4,dispatch_at=$5,readiness_at=$6,ends_at=$7,origin_region_code=$8,destination_region_code=$9,free_cancellation_until=$10,pickup_window_end=$11,rider_reminder_at=$12,rider_reminder_sent_at=NULL,status='open',driver_id=NULL,ready_at=NULL,reminder_sent_at=NULL,version=version+1,updated_at=NOW() WHERE id=$1::uuid`, id, b.FareID, p.Version, b.PickupAt, b.PickupAt.Add(-time.Duration(p.Dispatch)*time.Minute), b.PickupAt.Add(-time.Duration(p.Readiness)*time.Minute), b.PickupAt.Add(time.Duration(seconds)*time.Second+30*time.Minute), region, destRegion, b.PickupAt.Add(-time.Duration(p.FreeCancellation)*time.Minute), b.PickupAt.Add(time.Duration(p.PickupWindow)*time.Minute), b.PickupAt.Add(-time.Duration(p.RiderReminder)*time.Minute))
	if e == nil {
		_, e = tx.Exec(ctx, `UPDATE ride_fares SET consumed_at=NOW(),expires_at=$2 WHERE id=$1::uuid`, b.FareID, b.PickupAt.Add(10*time.Minute))
	}
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO scheduled_ride_amendments(booking_id,request_id,old_fare_id,new_fare_id,pickup_at,previous_driver_id,resulting_version) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6::uuid,$7)`, id, b.RequestID, oldFare, b.FareID, b.PickupAt, driver, version+1)
	}
	extra := []string{}
	if driver != nil {
		extra = append(extra, *driver)
	}
	if e == nil {
		e = scheduleEvent(ctx, tx, id, "scheduled.event.amended", extra...)
	}
	return e
}
