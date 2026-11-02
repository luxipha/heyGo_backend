package main

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/services/ride-agent-gateway/agent"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	token := os.Getenv("RIDE_AGENT_INTERNAL_TOKEN")
	if len(token) < 32 {
		log.Fatal("RIDE_AGENT_INTERNAL_TOKEN must be configured with at least 32 characters")
	}
	places, err := agent.ConfiguredPlaces()
	if err != nil && os.Getenv("DATABASE_URL") == "" {
		log.Fatal(err)
	}
	planner, plannerErr := agent.ConfiguredPlanner()
	if plannerErr != nil {
		log.Print("AI planner unavailable; deterministic destination search remains enabled")
	}
	if raw := os.Getenv("DATABASE_URL"); raw != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		pool, err := pgxpool.New(ctx, raw)
		cancel()
		if err != nil {
			log.Fatal("Agent settings database unavailable")
		}
		defer pool.Close()
		planner = agent.ControlledPlanner{Pool: pool}
		places = agent.DatabasePlaces{Pool: pool}
	}
	gateway := &agent.Gateway{InternalToken: token, Places: places, LLMEnabled: os.Getenv("RIDE_AGENT_LLM_ENABLED") == "true" && plannerErr == nil, Planner: planner}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8085"
	}
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":" + port
	}
	server := &http.Server{Addr: addr, Handler: gateway, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	log.Fatal(server.ListenAndServe())
}
