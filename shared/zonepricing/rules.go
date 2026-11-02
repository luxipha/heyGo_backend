package zonepricing

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"math"
)

type Rule struct {
	ZoneID   string `json:"zoneId"`
	ZoneName string `json:"zoneName"`
	Kind     string `json:"kind"`
	Value    int64  `json:"value"`
}

func Valid(r Rule) bool {
	return r.ZoneID != "" && ((r.Kind == "multiplier" && r.Value >= 1000 && r.Value <= 50000) || (r.Kind == "fixed" && r.Value >= 0 && r.Value <= 1000000))
}
func Apply(amount float64, rule *Rule) (float64, bool) {
	if rule != nil {
		if !Valid(*rule) {
			return 0, false
		}
		if rule.Kind == "fixed" {
			amount += float64(rule.Value)
		} else {
			amount = math.Round(amount * float64(rule.Value) / 10000)
		}
	}
	return amount, !math.IsNaN(amount) && !math.IsInf(amount, 0) && amount > 0 && amount <= 10000000
}

// Rules use the reviewed zone geometry frozen with the pricing version.
func Match(ctx context.Context, pool *pgxpool.Pool, pricing string, lat, lng float64) (*Rule, error) {
	rows, err := pool.Query(ctx, `SELECT zone_id::text,zone_name,kind,value FROM pricing_zone_rules WHERE pricing_id=$1::uuid AND ST_Covers(boundary,ST_SetSRID(ST_MakePoint($2,$3),4326))`, pricing, lng, lat)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result *Rule
	for rows.Next() {
		if result != nil {
			return nil, fmt.Errorf("overlapping zone pricing")
		}
		r := Rule{}
		if err = rows.Scan(&r.ZoneID, &r.ZoneName, &r.Kind, &r.Value); err != nil {
			return nil, err
		}
		result = &r
	}
	return result, rows.Err()
}
