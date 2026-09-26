package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/luxipha/heyGo_backend/shared/db"
)

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	pool, err := db.NewPostgresPool(ctx, db.Config{URL: databaseURL, MaxConns: 2, MinConns: 1, MaxConnIdleTime: time.Minute})
	if err != nil {
		log.Fatalf("connect to PostgreSQL: %v", err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		log.Fatalf("run PostgreSQL migrations: %v", err)
	}
	log.Println("PostgreSQL migrations complete")
}
