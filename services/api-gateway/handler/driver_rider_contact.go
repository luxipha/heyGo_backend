package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

func (a *driverAPI) tripRiderContact(c *gin.Context) {
	id := c.Param("tripID")
	if _, err := uuid.Parse(id); err != nil {
		driverError(c, 400, "invalid_trip_id", "Invalid trip")
		return
	}
	var phone string
	err := a.pool.QueryRow(c.Request.Context(), `SELECT COALESCE(u.phone_e164,'') FROM trips t JOIN users u ON u.id=t.rider_id WHERE t.id=$1::uuid AND t.assigned_driver_id=$2::uuid AND t.status IN ('accepted','arrived','started') AND u.active`, id, driverID(c)).Scan(&phone)
	if err != nil || phone == "" {
		driverError(c, 404, "rider_phone_unavailable", "Rider phone number is unavailable for this trip")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, contracts.APIResponse{Data: gin.H{"phone": phone}})
}
