package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"reflect"
	"strings"
)

func (dc *DriverConsumer) handleRiderCommand(ctx context.Context, command string, p messaging.TripLifecycleCommand) error {
	if dc.pool == nil {
		return fmt.Errorf("rider command storage unavailable")
	}
	if _, err := uuid.Parse(p.CommandID); err != nil {
		return err
	}
	var stored []byte
	var state string
	err := dc.pool.QueryRow(ctx, `SELECT payload,status FROM rider_trip_commands WHERE id=$1::UUID AND rider_id=$2::UUID AND trip_id=$3::UUID AND command=$4`, p.CommandID, p.ActorID, p.TripID, command).Scan(&stored, &state)
	if err != nil {
		return err
	}
	var original messaging.TripLifecycleCommand
	if json.Unmarshal(stored, &original) != nil || !reflect.DeepEqual(original, p) {
		return fmt.Errorf("rider command payload mismatch")
	}
	if state != "queued" {
		return nil
	}
	err = dc.handleLifecycle(ctx, command, p)
	outcome, reason := "confirmed", ""
	if err != nil {
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		policyError := strings.Contains(err.Error(), "trip cannot be cancelled") || strings.Contains(err.Error(), "completed trip is not accessible") || strings.Contains(err.Error(), "only riders may") || strings.Contains(err.Error(), "rider ratings do not")
		if !policyError {
			return err
		}
		outcome, reason = "rejected", "trip_state_changed"
	} else if command == contracts.TripCmdCancel {
		var matches bool
		if err := dc.pool.QueryRow(ctx, `SELECT status='cancelled' AND COALESCE(cancellation_actor_id=$2::UUID,FALSE) AND cancellation_reason=$3 FROM trips WHERE id=$1::UUID`, p.TripID, p.ActorID, p.Reason).Scan(&matches); err != nil {
			return err
		}
		if !matches {
			outcome, reason = "rejected", "trip_state_changed"
		}
	} else if command == contracts.TripCmdRate {
		var matches bool
		if err := dc.pool.QueryRow(ctx, `SELECT rating=$3 AND COALESCE(comment,'')=$4 FROM trip_ratings WHERE trip_id=$1::UUID AND actor_id=$2::UUID`, p.TripID, p.ActorID, p.Rating, p.Comment).Scan(&matches); err != nil {
			return err
		}
		if !matches {
			outcome, reason = "rejected", "rating_already_submitted"
		}
	}
	tx, err := dc.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE rider_trip_commands SET status=$2,reason=NULLIF($3,''),resolved_at=NOW() WHERE id=$1::UUID AND status='queued'`, p.CommandID, outcome, reason)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 1 {
		data, _ := json.Marshal(map[string]any{"commandId": p.CommandID, "tripId": p.TripID, "command": command, "status": outcome, "outcome": outcome, "reason": reason})
		if _, err := messaging.AppendEventTx(ctx, tx, p.ActorID, "rider-command:"+p.CommandID, "trip.event.command_result", data); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
