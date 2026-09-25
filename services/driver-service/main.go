package main

import (
	"context"
	"log"
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
	"github.com/luxipha/heyGo_backend/shared/messaging/kafka"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	"github.com/luxipha/heyGo_backend/shared/observe/traces"
)

var (
	brokers = env.GetCSV("KAFKA_BROKERS", []string{"apache-kafka:9092"})
	groupID = "driver-service-group"
	topics  = []string{contracts.TripEventCreated, contracts.TripEventDriverNotInterested, contracts.DriverCmdTripDecline, contracts.DriverCmdLocation}
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

	// Initialize Kafka client
	kfClient, err := kafka.NewKafkaClient(brokers, groupID)
	if err != nil {
		logs.L().Fatalw("Failed to create Kafka client", "error", err)
	}
	defer kfClient.Close()
	logs.L().Info("Kafka client connected")
	if err := kfClient.EnsureTopics(ctx, brokers, kafka.DefaultTopics(), env.GetInt("KAFKA_TOPIC_PARTITIONS", 3), env.GetInt("KAFKA_REPLICATION_FACTOR", 1)); err != nil {
		logs.L().Fatalw("Failed to provision Kafka topics", "error", err)
	}

	// Initialize repositories and services
	databaseURL := env.GetString("DATABASE_URL", "")
	if databaseURL == "" {
		logs.L().Fatal("DATABASE_URL is required")
	}
	pool, err := db.NewPostgresPool(ctx, db.Config{URL: databaseURL, MaxConnIdleTime: 5 * time.Minute, MaxConns: 10, MinConns: 1})
	if err != nil {
		logs.L().Fatalw("Failed to connect to PostgreSQL", "error", err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		logs.L().Fatalw("Failed to run PostgreSQL migrations", "error", err)
	}
	driverRepo := repo.NewPostgresDriverRepository(pool)
	driverService := service.NewDriverService(driverRepo)
	go messaging.PublishOutbox(ctx, pool, kfClient.Producer)

	// Start consuming trip events
	tripConsumer := events.NewTripConsumer(kfClient, driverService)
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

	// Start gRPC server
	healthCheck := func(checkCtx context.Context) error {
		if err := pool.Ping(checkCtx); err != nil {
			return err
		}
		return kfClient.Ping(checkCtx)
	}
	grpcServer := NewgRPCServer(":9100", kfClient, driverService, healthCheck)
	go func() {
		if err := grpcServer.run(ctx); err != nil && ctx.Err() == nil {
			logs.L().Errorw("gRPC server error", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	logs.L().Info("Shutdown signal received, exiting...")
}
