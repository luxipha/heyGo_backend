package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/services/api-gateway/handler"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/messaging/pubsub"
	"github.com/luxipha/heyGo_backend/shared/observe/logs"
	"github.com/luxipha/heyGo_backend/shared/storage"
)

type httpServer struct {
	addr        string
	bus         *pubsub.Client
	connManager *messaging.ConnectionManager
	auth        *gatewayauth.Middleware
	users       gatewayauth.UserStore
	pool        *pgxpool.Pool
	files       storage.ObjectStore
	origins     []string
	oauth       handler.CasperIDOAuthConfig
	readiness   func(context.Context) error
}

// NewhttpServer creates a new http server instance
func NewhttpServer(addr string, bus *pubsub.Client, connMgr *messaging.ConnectionManager, authMiddleware *gatewayauth.Middleware, users gatewayauth.UserStore, pool *pgxpool.Pool, files storage.ObjectStore, origins []string, oauth handler.CasperIDOAuthConfig, readiness func(context.Context) error) *httpServer {
	return &httpServer{addr: addr, bus: bus, connManager: connMgr, auth: authMiddleware, users: users, pool: pool, files: files, origins: origins, oauth: oauth, readiness: readiness}
}

// run starts the http server
func (s *httpServer) run(ctx context.Context) error {
	// http server setup
	h := handler.NewHTTPHandler(s.bus, s.connManager, s.auth, s.users, s.pool, s.files, s.origins, s.oauth, s.readiness)
	srv := &http.Server{
		Addr:              s.addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	// Start the server in a separate goroutine
	errCh := make(chan error, 1)
	go func() {
		logs.L().Infow("http server running", "addr", s.addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	// Wait for context cancellation or server error
	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("http server error: %w", err)
		}
	}

	// Graceful shutdown with a timeout
	shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(shCtx); err != nil {
		return fmt.Errorf("http server shutdown error: %w", err)
	}

	logs.L().Info("http server gracefully stopped")
	return nil
}
