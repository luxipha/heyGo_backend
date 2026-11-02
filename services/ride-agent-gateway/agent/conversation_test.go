package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBookingLanguageResolvesOnlyDestination(t *testing.T) {
	for _, message := range []string{"Book me a ride to Lekki", "book a ride to Lekki", "I need a ride to Lekki", "Can you take me to Lekki"} {
		places := &fakePlaces{places: []Place{fixture()}}
		reply, err := (&Gateway{Places: places}).Resolve(context.Background(), Request{SchemaVersion: 1, Message: message})
		if err != nil || reply.State != "resolved" || places.query != "Lekki" || !reply.ConfirmationRequired {
			t.Fatalf("%s: %#v %s %v", message, reply, places.query, err)
		}
	}
	for _, message := range []string{"book me a ride to admin account", "book a ride to wallet balance", "book me a ride to OTP 12345678"} {
		places := &fakePlaces{}
		model := &fakePlanner{}
		reply, _ := (&Gateway{Places: places, Planner: model, LLMEnabled: true}).Resolve(context.Background(), Request{SchemaVersion: 1, Message: message})
		if reply.State != "unsupported" || places.calls != 0 || model.calls != 0 {
			t.Fatalf("sensitive content reached tools: %s", message)
		}
	}
}
func TestClarificationNeverBecomesPlaceSearch(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		places := &fakePlaces{}
		model := &fakePlanner{}
		reply, err := (&Gateway{Places: places, Planner: model, LLMEnabled: enabled}).Resolve(context.Background(), Request{SchemaVersion: 1, Message: "Hello!"})
		if err != nil || reply.State != "clarification" || places.calls != 0 || model.calls != 0 {
			t.Fatalf("greeting caused lookup: %#v", reply)
		}
	}
	places := &fakePlaces{}
	model := &fakePlanner{plan: Plan{Tool: "clarify_destination"}}
	reply, err := (&Gateway{Places: places, Planner: model, LLMEnabled: true}).Resolve(context.Background(), Request{SchemaVersion: 1, Message: "Tell me a joke"})
	if err != nil || reply.State != "clarification" || places.calls != 0 || model.calls != 1 || !reply.ConfirmationRequired {
		t.Fatalf("unrelated message caused lookup: %#v", reply)
	}
}
func TestGeminiClarificationProposalHasNoArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"clarify_destination","args":{}}}]}}]}`))
	}))
	defer server.Close()
	plan, err := (GeminiPlanner{Key: "test", Model: "gemini-2.5-flash-lite", endpoint: server.URL}).Plan(context.Background(), "Tell me a joke")
	if err != nil || plan.Tool != "clarify_destination" || plan.Query != "" {
		t.Fatalf("invalid clarification: %#v %v", plan, err)
	}
	if ValidatePlan(Plan{Tool: "clarify_destination", Query: "some fabricated address"}) == nil {
		t.Fatal("clarification accepted search arguments")
	}
}
