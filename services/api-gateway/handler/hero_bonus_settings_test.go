package handler

import "testing"

func TestValidHeroesSettings(t *testing.T) {
	ok := heroesSettings{AcceptingNew: true, LiveBonusKobo: 50000, OrdersBonusKobo: 50000, OrdersBonusCount: 5, ReviewHoursMin: 5, ReviewHoursMax: 48}
	if !validHeroesSettings(ok) {
		t.Fatal("defaults must be valid")
	}
	bad := map[string]func(*heroesSettings){
		"negative bonus":     func(s *heroesSettings) { s.LiveBonusKobo = -1 },
		"huge bonus":         func(s *heroesSettings) { s.OrdersBonusKobo = 10000001 },
		"zero order count":   func(s *heroesSettings) { s.OrdersBonusCount = 0 },
		"huge order count":   func(s *heroesSettings) { s.OrdersBonusCount = 1001 },
		"min above max":      func(s *heroesSettings) { s.ReviewHoursMin = 49 },
		"max beyond 30 days": func(s *heroesSettings) { s.ReviewHoursMax = 721 },
	}
	for name, mutate := range bad {
		s := ok
		mutate(&s)
		if validHeroesSettings(s) {
			t.Errorf("%s: expected rejection", name)
		}
	}
}
