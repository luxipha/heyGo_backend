package agent

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/agentsettings"
	"os"
	"strings"
)

// ControlledPlanner fails closed for AI on unavailable configuration/budget;
// Gateway's existing deterministic/guided fallback remains available.
type ControlledPlanner struct {
	Pool *pgxpool.Pool
}

func (p ControlledPlanner) Plan(ctx context.Context, q string) (Plan, error) {
	return p.call(ctx, q, nil)
}
func (p ControlledPlanner) PlanTurn(ctx context.Context, q string, t TurnContext) (Plan, error) {
	return p.call(ctx, q, &t)
}
func PlannerFor(s agentsettings.Settings) (Planner, error) {
	if e := s.Validate(); e != nil {
		return nil, e
	}
	switch s.Provider {
	case "gemini":
		key := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
		if key == "" || !modelName.MatchString(s.Model) {
			return nil, errors.New("Gemini unavailable")
		}
		return GeminiPlanner{Key: key, Model: s.Model}, nil
	case "qwen":
		key := strings.TrimSpace(os.Getenv("ALIBABA_API_KEY"))
		if key == "" || !qwenModel.MatchString(s.Model) || !validQwenURL(s.BaseURL) {
			return nil, errors.New("Qwen unavailable")
		}
		return QwenPlanner{Key: key, Model: s.Model, BaseURL: s.BaseURL}, nil
	}
	return nil, errors.New("planner unavailable")
}
func (p ControlledPlanner) call(ctx context.Context, q string, t *TurnContext) (Plan, error) {
	readCtx, cancel := context.WithTimeout(ctx, agentsettings.Default().Timeout())
	defer cancel()
	status, e := agentsettings.Read(readCtx, p.Pool)
	if e != nil {
		return Plan{}, errors.New("planner controls unavailable")
	}
	if status.Version == 0 {
		return Plan{}, errors.New("staff planner configuration required")
	}
	s := status.Settings
	if !s.Enabled {
		return Plan{}, errors.New("planner disabled")
	}
	planner, e := PlannerFor(s)
	if e != nil {
		return Plan{}, e
	}
	if e = agentsettings.Reserve(readCtx, p.Pool, s.DailyMaxCalls); e != nil {
		return Plan{}, e
	}
	callCtx, done := context.WithTimeout(ctx, s.Timeout())
	defer done()
	if t != nil {
		return planner.(TurnPlanner).PlanTurn(callCtx, q, *t)
	}
	return planner.Plan(callCtx, q)
}
