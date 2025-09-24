package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/cprakhar/uber-clone/services/trip-service/events"
	"github.com/cprakhar/uber-clone/services/trip-service/repo"
	"github.com/cprakhar/uber-clone/services/trip-service/service"
	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/cprakhar/uber-clone/shared/env"
	"github.com/cprakhar/uber-clone/shared/messaging/kafka"
	"github.com/cprakhar/uber-clone/shared/observe/logs"
	"github.com/cprakhar/uber-clone/shared/observe/traces"
)

var (
	brokers = []string{"apache-kafka:9092"}
	groupID = "trip-service-group"
	topics  = []string{contracts.DriverCmdTripAccept, contracts.DriverCmdTripDecline}
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

	// Initialize Kafka client
	kfClient, err := kafka.NewKafkaClient(brokers, groupID)
	if err != nil {
		logs.L().Fatalw("Failed to create Kafka client", "error", err)
	}
	defer kfClient.Close()
	logs.L().Infof("Kafka client connected")

	// Initialize repositories and services
	tripRepo := repo.NewInMemoRepository()
	tripService := service.NewService(tripRepo)

	// Start consuming driver responses
	driverConsumer := events.NewDriverConsumer(kfClient, tripService)
	go func() {
		if err := driverConsumer.Consume(ctx, topics); err != nil {
			logs.L().Warnw("Error consuming driver topics", "error", err)
		}
	}()

	// Start gRPC server
	gRPCServer := NewgRPCServer(":9000", tripService, kfClient)
	go func() {
		if err := gRPCServer.run(ctx); err != nil && ctx.Err() == nil {
			logs.L().Errorw("gRPC server error", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	logs.L().Infof("Shutdown signal received, exiting...")
}
