package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/services/api-gateway/handler"
	sharedauth "github.com/luxipha/heyGo_backend/shared/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/db"
	"github.com/luxipha/heyGo_backend/shared/env"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/messaging/kafka"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	"github.com/luxipha/heyGo_backend/shared/observe/traces"
	"github.com/luxipha/heyGo_backend/shared/storage"
)

var (
	httpAddr = env.ListenAddr("HTTP_ADDR", ":8080")
	brokers  = env.GetCSV("KAFKA_BROKERS", []string{"apache-kafka:9092"})
	groupID  = "api-gateway-group"
	topics   = []string{
		contracts.TripEventNoDriversFound,
		contracts.TripEventDriverAssigned,
		contracts.DriverCmdTripRequest,
		contracts.PaymentEventSessionCreated,
		contracts.PaymentEventSuccess,
		contracts.PaymentEventFailed,
		contracts.PaymentEventCancelled,
		contracts.TripEventStarted,
		contracts.TripEventArrived,
		contracts.TripEventCompleted,
		contracts.TripEventCancelled,
		contracts.TripEventSettlementUpdated,
		contracts.TripEventNoShowClaimUpdated,
		contracts.TripEventExpired,
		contracts.TripEventReassigned,
		contracts.DriverEventCommandAcknowledged,
		contracts.DriverEventLocationUpdated,
	}
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

	databaseURL := env.GetString("DATABASE_URL", "")
	casperIDAppID := env.GetString("CASPERID_APP_ID", "")
	internalServiceToken := env.GetString("INTERNAL_SERVICE_TOKEN", "")
	if databaseURL == "" || casperIDAppID == "" || internalServiceToken == "" {
		logs.L().Fatal("DATABASE_URL, CASPERID_APP_ID, and INTERNAL_SERVICE_TOKEN are required")
	}
	pool, err := db.NewPostgresPool(ctx, db.Config{URL: databaseURL, MaxConnIdleTime: 5 * time.Minute, MaxConns: 10, MinConns: 1})
	if err != nil {
		logs.L().Fatalw("Failed to connect to PostgreSQL", "error", err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		logs.L().Fatalw("Failed to run PostgreSQL migrations", "error", err)
	}

	origins := splitCSV(env.GetString("ALLOWED_ORIGINS", "http://localhost:3000"))
	connManager := messaging.NewConnectionManager(origins...)
	users := gatewayauth.NewPostgresUserStore(pool)
	verifier := sharedauth.NewCasperIDVerifier(
		env.GetString("CASPERID_JWKS_URL", "https://casperid.com/.well-known/jwks.json"),
		env.GetString("CASPERID_ISSUER", "casperid.com"),
		casperIDAppID,
	)
	authMiddleware := gatewayauth.NewMiddleware(verifier, users)
	var files storage.ObjectStore
	if endpoint, bucket, accessKey, secretKey := env.GetString("R2_ENDPOINT", ""), env.GetString("R2_BUCKET", ""), env.GetString("R2_ACCESS_KEY_ID", ""), env.GetString("R2_SECRET_ACCESS_KEY", ""); endpoint != "" || bucket != "" || accessKey != "" || secretKey != "" {
		files, err = storage.NewR2Store(storage.R2Config{Endpoint: endpoint, Bucket: bucket, AccessKey: accessKey, SecretKey: secretKey})
		if err != nil {
			logs.L().Fatalw("invalid R2 configuration", "error", err)
		}
	}
	handler.StartPrivacyJobs(ctx, pool, files)
	handler.StartNotificationPushJobs(ctx, pool)

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

	topicConsumer := messaging.NewTopicConsumer(kfClient, connManager, topics, messaging.NewEventStore(pool))
	go func() {
		if err := topicConsumer.Consume(ctx); err != nil && ctx.Err() == nil {
			logs.L().Warnw("Error consuming topics", "error", err)
		}
	}()

	// Start http server
	readiness := func(checkCtx context.Context) error {
		if err := pool.Ping(checkCtx); err != nil {
			return err
		}
		if err := kfClient.Ping(checkCtx); err != nil {
			return err
		}
		return verifier.Ping(checkCtx)
	}
	oauthConfig := handler.CasperIDOAuthConfig{
		AppID:             casperIDAppID,
		APISecret:         env.GetString("CASPERID_API_SECRET", ""),
		TokenURL:          env.GetString("CASPERID_TOKEN_URL", "https://apis.casperid.com/api/oauth/token"),
		DriverRedirectURI: env.GetString("CASPERID_DRIVER_REDIRECT_URI", "com.heygo.driver://oauth/callback"),
	}
	httpServer := NewhttpServer(httpAddr, kfClient, connManager, authMiddleware, users, pool, files, origins, oauthConfig, readiness)
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

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(strings.TrimSuffix(part, "/")); part != "" {
			result = append(result, part)
		}
	}
	return result
}
