package main

import (
	"context"
	"log"
	"net/http"
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
	"github.com/luxipha/heyGo_backend/shared/messaging/pubsub"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	"github.com/luxipha/heyGo_backend/shared/observe/traces"
	"github.com/luxipha/heyGo_backend/shared/storage"
)

var (
	httpAddr  = env.ListenAddr("HTTP_ADDR", ":8080")
	projectID = env.GetString("GCP_PROJECT_ID", env.GetString("GOOGLE_CLOUD_PROJECT", "heygo-ng"))
	groupID   = "api-gateway-group"
	topics    = []string{
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
	pool, err := db.NewPostgresPool(ctx, db.Config{
		URL:             databaseURL,
		MaxConnIdleTime: 5 * time.Minute,
		MaxConns:        int32(env.GetInt("DB_MAX_CONNS", 5)),
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

	origins := splitCSV(env.GetString("ALLOWED_ORIGINS", "http://localhost:3000"))
	connManager := messaging.NewConnectionManager(origins...)
	eventNotifier := messaging.NewEventNotifier(pool)
	go eventNotifier.Run(ctx)
	eventStore := messaging.NewEventStore(pool, eventNotifier)
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
	// Initialize the Pub/Sub client. Topics and subscriptions are provisioned by Terraform.
	bus, err := pubsub.NewClient(ctx, projectID, groupID)
	if err != nil {
		logs.L().Fatalw("Failed to create Pub/Sub client", "error", err)
	}
	defer bus.Close()
	logs.L().Info("Pub/Sub client connected")

	topicConsumer := messaging.NewTopicConsumer(bus, connManager, topics, eventStore)
	var pushHandler http.Handler
	switch deliveryMode := env.GetString("PUBSUB_DELIVERY_MODE", "pull"); deliveryMode {
	case "pull":
		handler.StartPrivacyJobs(ctx, pool, files)
		handler.StartNotificationPushJobs(ctx, pool)
		go func() {
			if err := topicConsumer.Consume(ctx); err != nil && ctx.Err() == nil {
				logs.L().Warnw("Error consuming topics", "error", err)
			}
		}()
	case "push":
		authorizer := sharedauth.NewPubSubPushAuthorizer(
			env.GetString("PUBSUB_PUSH_AUDIENCE", ""),
			env.GetString("PUBSUB_PUSH_SERVICE_ACCOUNT", ""),
		)
		pushMux := pubsub.NewPushMux(topics, topicConsumer.Handle, authorizer)
		pushMux.Handle("POST /internal/tasks/maintenance", pubsub.AuthorizeHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handler.RunPrivacyJobsOnce(r.Context(), pool, files, 25)
			if err := handler.RunNotificationPushJobsOnce(r.Context(), pool, 100); err != nil {
				logs.L().Warnw("Scheduled gateway maintenance failed", "error", err)
				http.Error(w, "maintenance failed", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}), authorizer))
		pushHandler = pushMux
	default:
		logs.L().Fatalw("Unsupported Pub/Sub delivery mode", "mode", deliveryMode)
	}

	// Start http server
	readiness := func(checkCtx context.Context) error {
		if err := pool.Ping(checkCtx); err != nil {
			return err
		}
		if err := bus.Ping(checkCtx); err != nil {
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
	httpServer := NewhttpServer(httpAddr, bus, connManager, authMiddleware, users, pool, eventStore, files, origins, oauthConfig, readiness, pushHandler)
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
