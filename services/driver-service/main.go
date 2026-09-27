package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/luxipha/heyGo_backend/services/driver-service/events"
	"github.com/luxipha/heyGo_backend/services/driver-service/repo"
	"github.com/luxipha/heyGo_backend/services/driver-service/service"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/env"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/messaging/pubsub"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	"github.com/luxipha/heyGo_backend/shared/observe/traces"
	sharedtasks "github.com/luxipha/heyGo_backend/shared/tasks"
)

var (
	projectID = env.GetString("GCP_PROJECT_ID", env.GetString("GOOGLE_CLOUD_PROJECT", "heygo-ng"))
	grpcAddr  = env.ListenAddr("GRPC_ADDR", ":9100")
	groupID   = "driver-service-group"
	topics    = []string{contracts.TripEventCreated, contracts.TripEventDriverNotInterested, contracts.DriverCmdTripDecline, contracts.DriverCmdLocation}
)

func main() {
	logger, err := logs.Init("driver-service")
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Sync()
	logs.L().Info("Logger initialized")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Initialize OpenTelemetry
	otelCfg := traces.Config{
		ServiceName:      "driver-service",
		Environment:      env.GetString("ENVIRONMENT", "development"),
		Secure:           env.GetString("OTEL_SECURE", "false") == "true",
		ExporterEndpoint: env.GetString("OTEL_EXPORTER_OTLP_ENDPOINT", "http://jaeger-collector:4318"),
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

	// Initialize the Pub/Sub client. Topics and subscriptions are provisioned by Terraform.
	bus, err := pubsub.NewClient(ctx, projectID, groupID)
	if err != nil {
		logs.L().Fatalw("Failed to create Pub/Sub client", "error", err)
	}
	defer bus.Close()
	logs.L().Info("Pub/Sub client connected")

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
	driverRepo := repo.NewPostgresDriverRepository(pool)
	driverService := service.NewDriverService(driverRepo)

	deliveryMode := env.GetString("PUBSUB_DELIVERY_MODE", "pull")
	tripConsumer := events.NewTripConsumer(bus, driverService)
	if deliveryMode == "push" {
		taskClient, err := sharedtasks.NewClient(ctx, sharedtasks.Config{
			ProjectID:          projectID,
			Location:           env.GetString("GCP_REGION", "europe-west1"),
			Queue:              env.GetString("OFFER_EXPIRY_QUEUE", "driver-offer-expiry"),
			OIDCServiceAccount: env.GetString("TASKS_INVOKER_SERVICE_ACCOUNT", ""),
		})
		if err != nil {
			logs.L().Fatalw("Failed to create Cloud Tasks client", "error", err)
		}
		tripConsumer = events.NewTripConsumer(bus, driverService, cloudTaskOfferScheduler{client: taskClient})
	}
	var pushHandler http.Handler
	switch deliveryMode {
	case "pull":
		go messaging.PublishOutbox(ctx, pool, bus.Producer)
		go func() {
			if err := tripConsumer.RunExpiryWorker(ctx); err != nil && ctx.Err() == nil {
				logs.L().Warnw("Driver offer expiry worker stopped", "error", err)
			}
		}()
		go func() {
			if err := tripConsumer.Consume(ctx, topics); err != nil {
				logs.L().Warnw("Error consuming trip topics", "error", err)
			}
		}()
	case "push":
		pushMux := pubsub.NewPushMux(topics, func(messageCtx context.Context, message *pubsub.Message) error {
			if err := tripConsumer.Handle(messageCtx, message); err != nil {
				return err
			}
			_, err := messaging.DrainOutbox(messageCtx, pool, bus.Producer, 100)
			return err
		}, nil)
		pushMux.Handle("POST /internal/tasks/offers/expire", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := driverService.ExpireOffers(r.Context()); err != nil {
				http.Error(w, "offer expiry failed", http.StatusServiceUnavailable)
				return
			}
			if _, err := messaging.DrainOutbox(r.Context(), pool, bus.Producer, 100); err != nil {
				http.Error(w, "offer expiry delivery failed", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}))
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
	grpcServer := NewgRPCServer(grpcAddr, bus, driverService, healthCheck, pushHandler)
	go func() {
		if err := grpcServer.run(ctx); err != nil && ctx.Err() == nil {
			logs.L().Errorw("gRPC server error", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	logs.L().Info("Shutdown signal received, exiting...")
}
