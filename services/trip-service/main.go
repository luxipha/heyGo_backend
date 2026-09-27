package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/luxipha/heyGo_backend/services/trip-service/events"
	"github.com/luxipha/heyGo_backend/services/trip-service/repo"
	"github.com/luxipha/heyGo_backend/services/trip-service/service"
	trustclient "github.com/luxipha/heyGo_backend/services/trip-service/trust"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/env"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/messaging/pubsub"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	"github.com/luxipha/heyGo_backend/shared/observe/traces"
)

var (
	projectID = env.GetString("GCP_PROJECT_ID", env.GetString("GOOGLE_CLOUD_PROJECT", "heygo-ng"))
	grpcAddr  = env.ListenAddr("GRPC_ADDR", ":9000")
	groupID   = "trip-service-group"
	topics    = []string{contracts.DriverCmdTripAccept, contracts.TripCmdArrive, contracts.TripCmdStart, contracts.TripCmdComplete, contracts.TripCmdCancel, contracts.TripCmdRate}
)

func main() {
	// Initialize logger
	logger, err := logs.Init("trip-service")
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Sync()
	logs.L().Infof("Logger initialized")

	// Initialize OpenTelemetry
	otelCfg := traces.Config{
		ServiceName:      "trip-service",
		Environment:      env.GetString("ENV", "development"),
		ExporterEndpoint: env.GetString("OTEL_EXPORTER_OTLP_ENDPOINT", "http://jaeger-collector:4318"),
		Secure:           env.GetBool("OTEL_SECURE", false),
	}

	traceShutdown, err := traces.InitTrace(otelCfg)
	if err != nil {
		logs.L().Warnw("Failed to initialize tracing", "error", err)
	} else {
		defer func() {
			if err := traceShutdown(context.Background()); err != nil {
				logs.L().Warnw("Failed to shutdown tracer", "error", err)
			}
		}()
		logs.L().Info("OpenTelemetry tracing initialized")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Initialize the Pub/Sub client. Topics and subscriptions are provisioned by Terraform.
	bus, err := pubsub.NewClient(ctx, projectID, groupID)
	if err != nil {
		logs.L().Fatalw("Failed to create Pub/Sub client", "error", err)
	}
	defer bus.Close()
	logs.L().Infof("Pub/Sub client connected")

	// Initialize repositories and services
	databaseURL := env.GetString("DATABASE_URL", "")
	if databaseURL == "" {
		logs.L().Fatal("DATABASE_URL is required")
	}
	pool, err := db.NewPostgresPool(ctx, db.Config{
		URL:             databaseURL,
		MaxConnIdleTime: 5 * time.Minute,
		MaxConns:        int32(env.GetInt("DB_MAX_CONNS", 4)),
		MinConns:        int32(env.GetInt("DB_MIN_CONNS", 0)),
	})
	if err != nil {
		logs.L().Fatalw("Failed to connect to PostgreSQL", "error", err)
	}
	defer pool.Close()
	if env.GetBool("MIGRATE_ON_STARTUP", true) {
		if err := db.Migrate(ctx, pool); err != nil {
			logs.L().Fatalw("Failed to run PostgreSQL migrations", "error", err)
		}
	}
	tripRepo := repo.NewPostgresRepository(pool)
	tripService := service.NewService(tripRepo)

	casperIDAppID := env.GetString("CASPERID_APP_ID", "")
	casperIDSecret := env.GetString("CASPERID_API_SECRET", "")
	if casperIDAppID == "" || casperIDSecret == "" {
		logs.L().Fatal("CASPERID_APP_ID and CASPERID_API_SECRET are required")
	}
	reporter := trustclient.NewCasperIDReporter(env.GetString("CASPERID_BASE_URL", "https://casperid.com"), casperIDAppID, casperIDSecret)
	driverConsumer := events.NewDriverConsumer(bus, tripService, reporter)
	var pushHandler http.Handler
	switch deliveryMode := env.GetString("PUBSUB_DELIVERY_MODE", "pull"); deliveryMode {
	case "pull":
		go messaging.PublishOutbox(ctx, pool, bus.Producer)
		go func() {
			if err := driverConsumer.Consume(ctx, topics); err != nil {
				logs.L().Warnw("Error consuming driver topics", "error", err)
			}
		}()
	case "push":
		pushMux := pubsub.NewPushMux(topics, func(messageCtx context.Context, message *pubsub.Message) error {
			if err := driverConsumer.Handle(messageCtx, message); err != nil {
				return err
			}
			_, err := messaging.DrainOutbox(messageCtx, pool, bus.Producer, 100)
			return err
		}, nil)
		pushMux.Handle("POST /internal/tasks/outbox/drain", messaging.OutboxDrainHandler(pool, bus.Producer))
		pushHandler = pushMux
	default:
		logs.L().Fatalw("Unsupported Pub/Sub delivery mode", "mode", deliveryMode)
	}

	// Start gRPC server
	healthCheck := func(checkCtx context.Context) error {
		if err := pool.Ping(checkCtx); err != nil {
			return err
		}
		return bus.Ping(checkCtx)
	}
	gRPCServer := NewgRPCServer(grpcAddr, tripService, bus, healthCheck, pushHandler, pool)
	go func() {
		if err := gRPCServer.run(ctx); err != nil && ctx.Err() == nil {
			logs.L().Errorw("gRPC server error", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	logs.L().Infof("Shutdown signal received, exiting...")
}
