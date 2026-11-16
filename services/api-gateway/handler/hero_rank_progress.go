package handler

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
)

type heroRankProgress struct {
	Rank             string `json:"rank"`
	RequiredDrivers  int    `json:"requiredDrivers"`
	MinimumTrips     int    `json:"minimumTrips"`
	QualifiedDrivers int    `json:"qualifiedDrivers"`
	Met              bool   `json:"met"`
}

func readHeroRankProgress(ctx context.Context, pool *pgxpool.Pool, hero string) ([]heroRankProgress, error) {
	rows, err := pool.Query(ctx, `SELECT rank,required_drivers,minimum_trips,qualified_drivers FROM hero_rank_progress WHERE hero_id=$1::uuid ORDER BY CASE rank WHEN 'E' THEN 0 WHEN 'D' THEN 1 WHEN 'C' THEN 2 WHEN 'B' THEN 3 WHEN 'A' THEN 4 END`, hero)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	progress := []heroRankProgress{}
	for rows.Next() {
		var p heroRankProgress
		if err = rows.Scan(&p.Rank, &p.RequiredDrivers, &p.MinimumTrips, &p.QualifiedDrivers); err != nil {
			return nil, err
		}
		p.Met = p.QualifiedDrivers >= p.RequiredDrivers
		progress = append(progress, p)
	}
	return progress, rows.Err()
}
func resolveHeroRank(progress []heroRankProgress) (string, any, int, int) {
	rank := "E"
	var next any
	qualified, minimum := 0, 0
	for _, p := range progress {
		if !p.Met {
			next = map[string]any{"rank": p.Rank, "target": p.RequiredDrivers, "remaining": p.RequiredDrivers - p.QualifiedDrivers, "qualifiedDrivers": p.QualifiedDrivers, "minimumTrips": p.MinimumTrips}
			qualified = p.QualifiedDrivers
			minimum = p.MinimumTrips
			break
		}
		rank = p.Rank
		qualified = p.QualifiedDrivers
		minimum = p.MinimumTrips
	}
	return rank, next, qualified, minimum
}
