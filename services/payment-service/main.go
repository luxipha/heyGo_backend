package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/cprakhar/uber-clone/services/payment-service/events"
	"github.com/cprakhar/uber-clone/services/payment-service/service"
	"github.com/cprakhar/uber-clone/services/payment-service/types"
	"github.com/cprakhar/uber-clone/shared/contracts"
	"github.com/cprakhar/uber-clone/shared/env"
	"github.com/cprakhar/uber-clone/shared/messaging/kafka"
	"github.com/cprakhar/uber-clone/shared/observe/logs"
	"github.com/cprakhar/uber-clone/shared/observe/traces"
)

var (
	brokers = []string{"apache-kafka:9092"}
	groupID = "payment-service-group"
	appURL  = env.GetString("APP_URL", "http://localhost:3000")
	topics  = []string{contracts.PaymentCmdCreateSession}
)

func main() {
	logger, err := logs.Init("payment-service")
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Sync()
	logs.L().Info("Logger initialized")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Initialize OpenTelemetry
	otelCfg := traces.Config{
		ServiceName:      "payment-service",
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

	kfClient, err := kafka.NewKafkaClient(brokers, groupID)
	if err != nil {
		logs.L().Fatalw("Failed to create Kafka client", "error", err)
	}
	defer kfClient.Close()
	logs.L().Info("Kafka client connected")

	stripeCfg := &types.PaymentConfig{
		StripeSecretKey: env.GetString("STRIPE_SECRET_KEY", ""),
		SuccessURL:      env.GetString("STRIPE_SUCCESS_URL", appURL+"?payment=success"),
		CancelURL:       env.GetString("STRIPE_CANCEL_URL", appURL+"?payment=cancel"),
	}

	if stripeCfg.StripeSecretKey == "" {
		logs.L().Fatal("STRIPE_SECRET_KEY is not set")
	}

	paymentProcessor := service.NewStripeClient(stripeCfg)
	paymentService := service.NewPaymentService(paymentProcessor)

	tripConsumer := events.NewTripConsumer(kfClient, paymentService)
	go func() {
		if err := tripConsumer.Consume(ctx, topics); err != nil && ctx.Err() == nil {
			logs.L().Warnw("Error consuming payment topics", "error", err)
		}
		stop()
	}()

	<-ctx.Done()
	logs.L().Info("Shutdown signal received, exiting...")
}
