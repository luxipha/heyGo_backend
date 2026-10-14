package main

import (
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
	gateway := &agent.Gateway{InternalToken: token, Places: agent.GooglePlaces{Key: os.Getenv("RIDE_AGENT_PLACES_KEY")}, LLMEnabled: os.Getenv("RIDE_AGENT_LLM_ENABLED") == "true", Planner: agent.HTTPPlanner{URL: os.Getenv("RIDE_AGENT_PLANNER_URL"), Token: os.Getenv("RIDE_AGENT_PLANNER_TOKEN")}}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8085"
	}
	server := &http.Server{Addr: ":" + port, Handler: gateway, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	log.Fatal(server.ListenAndServe())
}
