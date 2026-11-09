package handler

import (
	"context"
	"strings"
)

// The catalogue owns public names and seat limits; slugs remain stable booking
// identifiers and must not be title-cased to invent a display label.
func enrichFareCategory(ctx context.Context, db riderHomeDB, fareID, owner string, fare map[string]any) error {
	var name string
	var passengers int
	var coins int64
	if err := db.QueryRow(ctx, `SELECT c.name,c.passengers,f.gocoin_earn_coins FROM ride_fares f JOIN ride_categories c ON c.slug=f.package_slug WHERE f.id=$1::uuid AND f.rider_id=$2::uuid`, fareID, owner).Scan(&name, &passengers, &coins); err != nil {
		return err
	}
	fare["displayName"] = name
	fare["goCoinsOnCompletion"] = coins
	fare["passengers"] = passengers
	fare["imageKey"] = riderCategoryImageKey(name)
	return nil
}

// These keys identify bundled Rider illustrations, never remote images. Unknown
// or newly created catalogue categories receive the generic car illustration.
func riderCategoryImageKey(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "go comfort":
		return "go_comfort"
	case "go green":
		return "go_green"
	case "go xl":
		return "go_xl"
	case "go reserve":
		return "go_reserve"
	case "go vip":
		return "go_vip"
	default:
		return "go_standard"
	}
}
