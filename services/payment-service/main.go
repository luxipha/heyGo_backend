package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/luxipha/heyGo_backend/services/payment-service/events"
	"github.com/luxipha/heyGo_backend/services/payment-service/handler"
	"github.com/luxipha/heyGo_backend/services/payment-service/repo"
	"github.com/luxipha/heyGo_backend/services/payment-service/service"
	"github.com/luxipha/heyGo_backend/services/payment-service/types"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/env"
	"github.com/luxipha/heyGo_backend/shared/messaging/kafka"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	"github.com/luxipha/heyGo_backend/shared/observe/traces"
)

var (
	brokers  = env.GetCSV("KAFKA_BROKERS", []string{"apache-kafka:9092"})
	groupID  = "payment-service-group"
	appURL   = env.GetString("APP_URL", "http://localhost:3000")
	httpAddr = env.GetString("PAYMENT_HTTP_ADDR", ":9200")
	topics   = []string{contracts.PaymentCmdCreateSession}
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
	if err := kfClient.EnsureTopics(ctx, brokers, kafka.DefaultTopics(), env.GetInt("KAFKA_TOPIC_PARTITIONS", 3), env.GetInt("KAFKA_REPLICATION_FACTOR", 1)); err != nil {
		logs.L().Fatalw("Failed to provision Kafka topics", "error", err)
	}

	paymentCfg := &types.PaymentConfig{
		BaseURL:      env.GetString("MONNIFY_BASE_URL", "https://sandbox.monnify.com"),
		APIKey:       env.GetString("MONNIFY_API_KEY", ""),
		SecretKey:    env.GetString("MONNIFY_SECRET_KEY", ""),
		ContractCode: env.GetString("MONNIFY_CONTRACT_CODE", ""),
		RedirectURL:  env.GetString("MONNIFY_REDIRECT_URL", appURL+"?payment=pending"),
	}

	if paymentCfg.APIKey == "" || paymentCfg.SecretKey == "" || paymentCfg.ContractCode == "" {
		logs.L().Fatal("MONNIFY_API_KEY, MONNIFY_SECRET_KEY, and MONNIFY_CONTRACT_CODE are required")
	}
	internalServiceToken := env.GetString("INTERNAL_SERVICE_TOKEN", "")
	if internalServiceToken == "" {
		logs.L().Fatal("INTERNAL_SERVICE_TOKEN is required")
	}

	databaseURL := env.GetString("DATABASE_URL", "")
	if databaseURL == "" {
		logs.L().Fatal("DATABASE_URL is required")
	}
	databasePool, err := db.NewPostgresPool(ctx, db.Config{
		URL: databaseURL, MaxConnIdleTime: 30 * time.Second,
		MaxConns: 20, MinConns: 1,
	})
	if err != nil {
		logs.L().Fatalw("Failed to connect to PostgreSQL", "error", err)
	}
	defer databasePool.Close()
	if err := db.Migrate(ctx, databasePool); err != nil {
		logs.L().Fatalw("Failed to run PostgreSQL/PostGIS migrations", "error", err)
	}
	paymentRepo := repo.NewPostgresPaymentRepository(databasePool)

	paymentProcessor := service.NewMonnifyClient(paymentCfg)
	paymentService := service.NewPaymentService(paymentProcessor, paymentRepo)

	tripConsumer := events.NewTripConsumer(kfClient, paymentService)
	go func() {
		if err := tripConsumer.Consume(ctx, topics); err != nil && ctx.Err() == nil {
			logs.L().Warnw("Error consuming payment topics", "error", err)
		}
		stop()
	}()

	readiness := func(checkCtx context.Context) error {
		if err := databasePool.Ping(checkCtx); err != nil {
			return err
		}
		if err := kfClient.Ping(checkCtx); err != nil {
			return err
		}
		return paymentProcessor.Ping(checkCtx)
	}
	router := handler.NewHTTPHandlerWithTopups(paymentCfg.SecretKey, paymentService, kfClient,
		&handler.OperatingTopupHandler{Pool: databasePool, InternalToken: internalServiceToken,
			Initializer: paymentProcessor, Verifier: paymentProcessor}, readiness)
	go func() {
		if err := handler.ListenAndServe(ctx.Done(), httpAddr, router); err != nil && ctx.Err() == nil {
			logs.L().Errorw("Payment HTTP server failed", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	logs.L().Info("Shutdown signal received, exiting...")
}
