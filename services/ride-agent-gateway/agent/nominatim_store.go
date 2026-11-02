package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Shared across replicas so public requests remain below one per second for HeyGo.
type PostgresNominatimStore struct{ Pool *pgxpool.Pool }

func (s PostgresNominatimStore) Read(ctx context.Context, key string) ([]Place, bool, error) {
	if s.Pool == nil {
		return nil, false, errors.New("places cache unavailable")
	}
	var raw []byte
	err := s.Pool.QueryRow(ctx, `SELECT results FROM geo_place_cache WHERE cache_key=$1 AND expires_at>clock_timestamp()`, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var places []Place
	err = json.Unmarshal(raw, &places)
	return places, err == nil, err
}
func (s PostgresNominatimStore) Write(ctx context.Context, key string, places []Place) error {
	raw, err := json.Marshal(places)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO geo_place_cache(cache_key,results,expires_at) VALUES($1,$2,clock_timestamp()+interval '1 day') ON CONFLICT(cache_key) DO UPDATE SET results=EXCLUDED.results,expires_at=EXCLUDED.expires_at`, key, raw)
	return err
}
func (s PostgresNominatimStore) Reserve(ctx context.Context, upstream string) error {
	if s.Pool == nil {
		return errors.New("places rate limit unavailable")
	}
	var key string
	err := s.Pool.QueryRow(ctx, `INSERT INTO geo_place_upstreams(upstream,next_request_at) VALUES($1,clock_timestamp()+interval '1100 milliseconds') ON CONFLICT(upstream) DO UPDATE SET next_request_at=clock_timestamp()+interval '1100 milliseconds' WHERE geo_place_upstreams.next_request_at<=clock_timestamp() RETURNING upstream`, upstream).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("address search busy; retry shortly")
	}
	return err
}
func (s PostgresNominatimStore) Backoff(ctx context.Context, upstream string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE geo_place_upstreams SET next_request_at=GREATEST(next_request_at,clock_timestamp()+interval '1 hour') WHERE upstream=$1`, upstream)
	return err
}
