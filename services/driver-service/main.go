package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/cprakhar/uber-clone/services/driver-service/events"
	"github.com/cprakhar/uber-clone/services/driver-service/repo"
	"github.com/cprakhar/uber-clone/services/driver-service/service"
	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/cprakhar/uber-clone/shared/env"
	"github.com/cprakhar/uber-clone/shared/messaging/kafka"
	"github.com/cprakhar/uber-clone/shared/observe/logs"
	"github.com/cprakhar/uber-clone/shared/observe/traces"
)

var (
	brokers = []string{"apache-kafka:9092"}
	groupID = "driver-service-group"
	topics  = []string{contracts.TripEventCreated, contracts.TripEventDriverNotInterested}
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

	// Initialize repositories and services
	driverRepo := repo.NewDriverRepository()
	driverService := service.NewDriverService(driverRepo)

	// Start consuming trip events
	tripConsumer := events.NewTripConsumer(kfClient, driverService)
	go func() {
		if err := tripConsumer.Consume(ctx, topics); err != nil {
			logs.L().Warnw("Error consuming trip topics", "error", err)
		}
	}()

	// Start gRPC server
	grpcServer := NewgRPCServer(":9100", kfClient, driverService)
	go func() {
		if err := grpcServer.run(ctx); err != nil && ctx.Err() == nil {
			logs.L().Errorw("gRPC server error", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	logs.L().Info("Shutdown signal received, exiting...")
}
