package handler

import (
	"encoding/json"

	grpcclient "github.com/cprakhar/uber-clone/services/api-gateway/grpc-client"
	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/cprakhar/uber-clone/shared/messaging"
	"github.com/cprakhar/uber-clone/shared/messaging/kafka"
	"github.com/cprakhar/uber-clone/shared/observe/logs"
	"github.com/cprakhar/uber-clone/shared/proto/driver"
	"github.com/gin-gonic/gin"
)

// RidersWSHandler handles WebSocket connections for riders
func RidersWSHandler(ctx *gin.Context, kfc *kafka.KafkaClient, connManager *messaging.ConnectionManager) {
	conn, err := connManager.Upgrade(ctx.Writer, ctx.Request)
	if err != nil {
		logs.L().Errorw("websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	riderID := ctx.Query("riderID")
	if riderID == "" {
		logs.L().Info("No riderID provided")
		return
	}

	// Add the connection to the manager
	connManager.Add(riderID, conn)
	defer connManager.Remove(riderID)

	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			logs.L().Errorw("Error reading message", "error", err)
			break
		}
		logs.L().Infow("Received message to rider", "riderID", riderID)
	}
}

// DriversWSHandler handles WebSocket connections for drivers
func DriversWSHandler(ctx *gin.Context, kfc *kafka.KafkaClient, connManager *messaging.ConnectionManager) {
	conn, err := connManager.Upgrade(ctx.Writer, ctx.Request)
	if err != nil {
		logs.L().Errorw("websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	driverID := ctx.Query("driverID")
	if driverID == "" {
		logs.L().Info("No driverID provided")
		return
	}

	packageSlug := ctx.Query("packageSlug")
	if packageSlug == "" {
		logs.L().Info("No packageSlug provided")
		return
	}

	// Add the connection to the manager
	connManager.Add(driverID, conn)

	driverService, err := grpcclient.NewDriverServiceClient()
	if err != nil {
		logs.L().Fatal(err)
	}

	defer func() {
		connManager.Remove(driverID)

		driverService.Client.UnregisterDriver(ctx, &driver.RegisterDriverRequest{
			DriverID:    driverID,
			PackageSlug: packageSlug,
		})

		driverService.Close()
		logs.L().Infow("Driver unregistered", "driverID", driverID)
	}()

	driver, err := driverService.Client.RegisterDriver(ctx, &driver.RegisterDriverRequest{
		DriverID:    driverID,
		PackageSlug: packageSlug,
	})
	if err != nil {
		logs.L().Errorw("failed to register driver", "driverID", driverID, "error", err)
		return
	}

	msg := contracts.WSMessage{
		Type: contracts.DriverCmdRegister,
		Data: driver.Driver,
	}

	if err := connManager.SendMessage(driverID, msg); err != nil {
		logs.L().Errorw("failed to send register message to driver", "driverID", driverID, "error", err)
		return
	}

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			logs.L().Errorw("error reading message", "error", err)
			break
		}

		type driverMessage struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}

		var dm driverMessage
		if err := json.Unmarshal(message, &dm); err != nil {
			logs.L().Warnw("Failed to unmarshal message", "error", err)
			continue
		}

		switch dm.Type {
		case contracts.DriverCmdLocation:
			// Update driver location in the system
			continue
		case contracts.DriverCmdTripAccept, contracts.DriverCmdTripDecline:
			// Notify trip service about trip acceptance/decline
			if err := kfc.Producer.SendMessage(ctx, dm.Type, &contracts.KafkaMessage{
				EntityID: driverID,
				Data:     dm.Data,
			}); err != nil {
				logs.L().Errorw("failed to send message to trip service", "error", err)
			}
		default:
			logs.L().Warnw("Unknown message from driver", "driverID", driverID, "type", dm.Type)
		}
	}
}
