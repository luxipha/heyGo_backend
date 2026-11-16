package referrals

import "testing"

func TestHeroStageRulesValidation(t *testing.T) {
	makeSettings := func() Settings {
		s := Settings{DisabledNotice: "Paused", DriverHeadline: "Invite", RiderHeadline: "Invite", Rules: map[string]Rule{}}
		for _, key := range RuleKeys {
			s.Rules[key] = Rule{TripTarget: 1}
		}
		s.Rules["hero_driver"] = Rule{TripTarget: 70, RewardKobo: 5200000, Stages: []Stage{{TripTarget: 10, RewardKobo: 700000, ApprovalMode: "manual", PayoutMode: "manual"}, {TripTarget: 30, RewardKobo: 1000000, ApprovalMode: "automatic", PayoutMode: "monnify"}, {TripTarget: 50, RewardKobo: 1500000, ApprovalMode: "manual"}, {TripTarget: 70, RewardKobo: 2000000, ApprovalMode: "automatic"}}}
		return s
	}
	if err := makeSettings().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Settings)
	}{
		{"duplicate threshold", func(s *Settings) { s.Rules["hero_driver"].Stages[1].TripTarget = 10 }},
		{"decreasing threshold", func(s *Settings) { s.Rules["hero_driver"].Stages[1].TripTarget = 5 }},
		{"zero reward", func(s *Settings) { s.Rules["hero_driver"].Stages[0].RewardKobo = 0 }},
		{"unknown approval", func(s *Settings) { s.Rules["hero_driver"].Stages[0].ApprovalMode = "skip" }},
		{"unknown payout", func(s *Settings) { s.Rules["hero_driver"].Stages[0].PayoutMode = "skip" }},
		{"wrong aggregate", func(s *Settings) { r := s.Rules["hero_driver"]; r.RewardKobo++; s.Rules["hero_driver"] = r }},
		{"wrong final target", func(s *Settings) { r := s.Rules["hero_driver"]; r.TripTarget = 71; s.Rules["hero_driver"] = r }},
		{"other referral type", func(s *Settings) { s.Rules["driver_driver"] = s.Rules["hero_driver"] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := makeSettings()
			tc.mutate(&s)
			if s.Validate() == nil {
				t.Fatal("invalid rule accepted")
			}
		})
	}
}
