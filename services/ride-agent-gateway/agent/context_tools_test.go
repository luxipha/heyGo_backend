package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type toolProposalPlanner struct{ plan Plan }

func (p toolProposalPlanner) Plan(context.Context, string) (Plan, error) { return p.plan, nil }
func (p toolProposalPlanner) PlanTurn(context.Context, string, TurnContext) (Plan, error) {
	return p.plan, nil
}
func TestContextualToolsOnlyProposeReadOrConfirmedReview(t *testing.T) {
	for _, tc := range []struct {
		tool   string
		turn   *TurnContext
		action bool
	}{
		{"preview_ride", nil, false},
		{"preview_ride", &TurnContext{}, false},
		{"preview_ride", &TurnContext{HasDestination: true}, true},
		{"request_ride", &TurnContext{HasDestination: true}, false},
		{"request_ride", &TurnContext{HasDestination: true, HasQuote: true}, true},
	} {
		places := &fakePlaces{}
		reply, err := (&Gateway{Places: places, LLMEnabled: true, Planner: toolProposalPlanner{Plan{Tool: tc.tool}}}).Resolve(context.Background(), Request{SchemaVersion: 1, Message: "Could you review this ride please", Context: tc.turn})
		if err != nil || !reply.ConfirmationRequired || places.calls != 0 {
			t.Fatalf("unexpected side effect: %#v %v", reply, err)
		}
		if (reply.State == "action") != tc.action {
			t.Fatalf("unexpected proposal: %#v", reply)
		}
	}
}
func TestGeminiRejectsRequestProposalWithoutQuoteContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"request_ride","args":{}}}]}}]}`))
	}))
	defer server.Close()
	planner := GeminiPlanner{Key: "test", Model: "gemini-2.5-flash-lite", endpoint: server.URL}
	if _, err := planner.PlanTurn(context.Background(), "review this ride", TurnContext{HasDestination: true}); err == nil {
		t.Fatal("request tool accepted without current quote context")
	}
	plan, err := planner.PlanTurn(context.Background(), "review this ride", TurnContext{HasDestination: true, HasQuote: true})
	if err != nil || plan.Tool != "request_ride" {
		t.Fatalf("valid review proposal failed: %#v %v", plan, err)
	}
}

func TestGuidedSearchNeverCallsConfiguredModel(t *testing.T) {
	places := &fakePlaces{places: []Place{fixture()}}
	model := &fakePlanner{plan: Plan{Tool: "clarify_destination"}}
	reply, err := (&Gateway{Places: places, Planner: model, LLMEnabled: true}).Resolve(context.Background(), Request{SchemaVersion: 1, Message: "Book me a ride to Lekki", Guided: true})
	if err != nil || reply.State != "resolved" || reply.Planner != "deterministic" || model.calls != 0 || places.query != "Lekki" {
		t.Fatalf("guided path invoked model or failed: %#v %v", reply, err)
	}
}

func TestSavedPlaceToolIsLabelOnlyAndRequiresTurnContext(t *testing.T) {
	for _, label := range []string{"Home", "Work"} {
		places := &fakePlaces{}
		reply, err := (&Gateway{Places: places, LLMEnabled: true, Planner: toolProposalPlanner{Plan{Tool: "resolve_saved_place", Query: label}}}).Resolve(context.Background(), Request{SchemaVersion: 1, Message: "take me back to my house", Context: &TurnContext{}})
		if err != nil || reply.Action != "resolve_saved_place" || reply.SavedPlaceLabel != label || places.calls != 0 || !reply.ConfirmationRequired {
			t.Fatalf("invalid saved place proposal: %#v %v", reply, err)
		}
	}
	if ValidatePlan(Plan{Tool: "resolve_saved_place", Query: "another user's address"}) == nil {
		t.Fatal("arbitrary saved-place label accepted")
	}
}

func TestUnavailableAIKeepsContextualRepliesOutOfPlaces(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		places := &fakePlaces{}
		// Invalid proposal represents failure/unsupported output and must not change
		// an existing route through a fresh Places search for conversational text.
		model := &fakePlanner{plan: Plan{Tool: "unknown_action"}}
		reply, err := (&Gateway{Places: places, Planner: model, LLMEnabled: enabled}).Resolve(context.Background(), Request{SchemaVersion: 1, Message: "Could I see the price for this trip?", Context: &TurnContext{HasDestination: true}})
		if err != nil || reply.State != "clarification" || places.calls != 0 {
			t.Fatalf("context fallback replaced destination: %#v %v", reply, err)
		}
	}
}
