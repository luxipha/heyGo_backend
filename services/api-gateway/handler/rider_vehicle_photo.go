package handler

import (
	"time"

	"github.com/gin-gonic/gin"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
)

// Reuse private vehicle-photo storage for the accepted car only. Inspection
// evidence such as VIN, interiors and walkaround videos is never Rider media.
func (a *riderHomeAPI) tripVehiclePhoto(c *gin.Context, tripID string) gin.H {
	if a.files == nil || a.pool == nil {
		return nil
	}
	user, _ := gatewayauth.CurrentUser(c)
	var key, vehicleID, driverID string
	err := a.pool.QueryRow(c.Request.Context(), `
SELECT p.storage_key,v.id::text,v.driver_id::text
FROM trips t
JOIN driver_vehicle_records v ON v.id=t.accepted_vehicle_id AND v.driver_id=t.assigned_driver_id
JOIN driver_vehicle_photos_records p ON p.vehicle_id=v.id AND p.driver_id=v.driver_id AND p.vehicle_revision=v.inspection_revision
WHERE t.id=$1::uuid AND t.rider_id=$2::uuid
 AND t.status IN ('accepted','arrived','started','completed')
 AND v.review_status='approved' AND v.approval_status='approved'
 AND EXISTS(SELECT 1 FROM driver_inspections_records i WHERE i.vehicle_id=v.id AND i.driver_id=v.driver_id AND i.vehicle_revision=v.inspection_revision AND i.status='completed')
 AND v.make=t.accepted_vehicle_snapshot->>'make' AND v.model=t.accepted_vehicle_snapshot->>'model'
 AND v.plate=t.accepted_vehicle_snapshot->>'plate' AND v.color=t.accepted_vehicle_snapshot->>'color'
 AND p.angle='front' AND p.content_type IN ('image/jpeg','image/png') AND p.superseded_at IS NULL
ORDER BY p.created_at DESC,p.id DESC LIMIT 1`, tripID, user.ID).Scan(&key, &vehicleID, &driverID)
	if err != nil {
		return nil
	}
	const ttl = 2 * time.Minute
	expires := time.Now().UTC().Add(ttl)
	url, err := a.files.PresignGet(c.Request.Context(), key, ttl)
	if err != nil {
		return nil
	}
	return gin.H{"url": url, "expiresAt": expires, "vehicleId": vehicleID, "driverId": driverID}
}
