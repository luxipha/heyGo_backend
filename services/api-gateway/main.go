package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/cprakhar/uber-clone/shared/env"
	"github.com/cprakhar/uber-clone/shared/messaging"
	"github.com/cprakhar/uber-clone/shared/messaging/kafka"
	"github.com/cprakhar/uber-clone/shared/observe/logs"
	"github.com/cprakhar/uber-clone/shared/observe/traces"
)

var (
	httpAddr = env.GetString("HTTP_ADDR", ":8080")
	brokers  = []string{"apache-kafka:9092"}
	groupID  = "api-gateway-group"
	topics   = []string{
		contracts.TripEventNoDriversFound,
		contracts.TripEventDriverAssigned,
		contracts.DriverCmdTripRequest,
		contracts.PaymentEventSessionCreated,
	}

	connManager = messaging.NewConnectionManager()
)

func main() {
	logger, err := logs.Init("api-gateway")
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Sync()
	logs.L().Info("Logger initialized")

	// Initialize OpenTelemetry
	otelCfg := traces.Config{
		ServiceName:      "api-gateway",
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
	logs.L().Info("Kafka client connected")

	topicConsumer := messaging.NewTopicConsumer(kfClient, connManager, topics)
	go func() {
		if err := topicConsumer.Consume(ctx); err != nil && ctx.Err() == nil {
			logs.L().Warnw("Error consuming topics", "error", err)
		}
	}()

	// Start http server
	httpServer := NewhttpServer(httpAddr, kfClient, connManager)
	go func() {
		if err := httpServer.run(ctx); err != nil && ctx.Err() == nil {
			logs.L().Errorw("http server error", "error", err)
			stop()
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	logs.L().Info("Shutdown signal received, exiting...")
}
