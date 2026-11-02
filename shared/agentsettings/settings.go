// Package agentsettings contains non-secret, staff-managed planner controls.
package agentsettings

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type Settings struct {
	Enabled       bool   `json:"enabled"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	BaseURL       string `json:"baseUrl"`
	TimeoutMS     int    `json:"timeoutMs"`
	DailyMaxCalls int    `json:"dailyMaxCalls"`
}
type Status struct {
	Settings  Settings `json:"settings"`
	Version   int64    `json:"version"`
	UsedToday int      `json:"usedToday"`
}

func Default() Settings {
	return Settings{Provider: "gemini", Model: "gemini-2.5-flash-lite", TimeoutMS: 3000, DailyMaxCalls: 1000}
}
func (s Settings) Validate() error {
	if s.Provider != "gemini" && s.Provider != "qwen" {
		return errors.New("provider must be gemini or qwen")
	}
	if len(s.Model) < 2 || len(s.Model) > 110 || len(s.BaseURL) > 250 || s.TimeoutMS < 300 || s.TimeoutMS > 3000 || s.DailyMaxCalls < 1 || s.DailyMaxCalls > 100000 {
		return errors.New("invalid planner limits or model")
	}
	return nil
}
func Read(ctx context.Context, p *pgxpool.Pool) (Status, error) {
	out := Status{Settings: Default()}
	var raw []byte
	e := p.QueryRow(ctx, `SELECT configuration,version,COALESCE((SELECT calls FROM agent_daily_usage WHERE day=(NOW() AT TIME ZONE 'UTC')::date),0) FROM admin_agent_settings WHERE id=1`).Scan(&raw, &out.Version, &out.UsedToday)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, nil
	}
	if e != nil {
		return out, e
	}
	e = json.Unmarshal(raw, &out.Settings)
	if e == nil {
		e = out.Settings.Validate()
	}
	return out, e
}

// Reserve before any provider call, including failed calls, atomically across replicas.
func Reserve(ctx context.Context, p *pgxpool.Pool, limit int) error {
	var count int
	e := p.QueryRow(ctx, `INSERT INTO agent_daily_usage(day,calls) VALUES((NOW() AT TIME ZONE 'UTC')::date,1) ON CONFLICT(day) DO UPDATE SET calls=agent_daily_usage.calls+1 WHERE agent_daily_usage.calls<$1 RETURNING calls`, limit).Scan(&count)
	if e != nil {
		return errors.New("planner budget unavailable or exhausted")
	}
	return nil
}
func (s Settings) Timeout() time.Duration { return time.Duration(s.TimeoutMS) * time.Millisecond }
