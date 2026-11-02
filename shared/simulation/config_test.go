package simulation

import (
	"math"
	"testing"
)

func TestParticipantConditions(t *testing.T) {
	base := Participant{Role: "driver", Name: "Simulation Driver", Controller: "simulator", Latitude: 6.428, Longitude: 3.421, Approval: "approved", AutoAccept: true}
	if e := base.Validate(); e != nil {
		t.Fatal(e)
	}
	cases := []struct {
		name   string
		mutate func(*Participant)
	}{{"arbitrary role", func(p *Participant) { p.Role = "admin" }}, {"unknown controller", func(p *Participant) { p.Controller = "both" }}, {"invalid coordinate", func(p *Participant) { p.Latitude = math.NaN() }}, {"overflow coordinate", func(p *Participant) { p.Longitude = 181 }}, {"invalid approval", func(p *Participant) { p.Approval = "eligible" }}, {"huge balance", func(p *Participant) { p.BalanceKobo = math.MaxInt64 }}, {"duplicate category", func(p *Participant) {
		p.CategoryIDs = []string{"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000001"}
	}}, {"model without year", func(p *Participant) { p.ModelID = "00000000-0000-4000-8000-000000000001" }}, {"driver fields on rider", func(p *Participant) { p.Role = "rider" }}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := base
			c.mutate(&p)
			if p.Validate() == nil {
				t.Fatal("invalid participant accepted")
			}
		})
	}
	rider := Participant{Role: "rider", Name: "Simulation Rider", Controller: "app", Latitude: 6.428, Longitude: 3.421}
	if e := rider.Validate(); e != nil {
		t.Fatal(e)
	}
}
