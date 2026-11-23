package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func validVenue() venueSubmission {
	return venueSubmission{VenueName: "Mama Put", Address: "12 Allen Ave, Ikeja", Phone: "08012345678", OwnerName: "Ada", KitchenType: "Bukka", Sells: []string{"Rice", "Bogus"}, OwnerPhone: "08087654321", Photos: []string{"data:image/jpeg;base64,AAAA"},
		Location: &struct {
			Lat float64 `json:"lat"`
			Lng float64 `json:"lng"`
		}{Lat: 6.5095, Lng: 3.3711}, LocationAccuracyM: 25}
}

func TestValidateVenueSubmission(t *testing.T) {
	v := validVenue()
	v.OwnerConfirmed = true
	if msg := validateVenueSubmission(&v); msg != "" {
		t.Fatalf("valid venue rejected: %s", msg)
	}
	if v.OwnerConfirmed {
		t.Fatal("client-supplied owner confirmation must not be trusted")
	}
	if v.Phone != "+2348012345678" || v.OwnerPhone != "+2348087654321" {
		t.Fatalf("phones not normalised: %s %s", v.Phone, v.OwnerPhone)
	}
	cases := map[string]func(*venueSubmission){
		"no name":          func(s *venueSubmission) { s.VenueName = " " },
		"bad phone":        func(s *venueSubmission) { s.Phone = "123" },
		"no owner":         func(s *venueSubmission) { s.OwnerName = "" },
		"no owner contact": func(s *venueSubmission) { s.OwnerPhone = ""; s.OwnerEmail = "" },
		"bad owner email":  func(s *venueSubmission) { s.OwnerEmail = "nope" },
		"no photo":         func(s *venueSubmission) { s.Photos = nil },
		"too many photos": func(s *venueSubmission) {
			s.Photos = []string{"data:image/png;base64,A", "data:image/png;base64,A", "data:image/png;base64,A", "data:image/png;base64,A"}
		},
		"url photo":      func(s *venueSubmission) { s.Photos = []string{"https://x.test/a.jpg"} },
		"no kitchen":     func(s *venueSubmission) { s.KitchenType = "" },
		"bad kitchen":    func(s *venueSubmission) { s.KitchenType = "Casino" },
		"bad menu photo": func(s *venueSubmission) { s.MenuPhoto = "https://x.test/m.jpg" },
		"no pin":         func(s *venueSubmission) { s.Location = nil },
		"vague pin":      func(s *venueSubmission) { s.LocationAccuracyM = 900 },
		"no accuracy":    func(s *venueSubmission) { s.LocationAccuracyM = 0 },
	}
	for name, mutate := range cases {
		s := validVenue()
		mutate(&s)
		if validateVenueSubmission(&s) == "" {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

func TestVenuePartnerClient(t *testing.T) {
	var gotKey, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotPath = r.Header.Get("x-heygo-service-key"), r.URL.Path
		switch {
		case r.URL.Path == "/api/heygo/venue-check":
			_, _ = w.Write([]byte(`{"success":true,"state":"locked","daysAgo":2,"daysLeft":5}`))
		case r.Method == http.MethodPost && r.Header.Get("x-test") == "":
			var s venueSubmission
			_ = json.NewDecoder(r.Body).Decode(&s)
			if s.VenueName == "Dup" {
				w.WriteHeader(409)
				_, _ = w.Write([]byte(`{"error":"A matching venue or submission already exists","duplicate":{"state":"locked","daysAgo":3,"daysLeft":4}}`))
				return
			}
			if s.VenueName == "Down" {
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"success":true,"submission":{"id":"s1","venueName":"Mama Put","stage":"waiting_for_owner","inviteLink":"https://x.test/claim/abc"}}`))
		default:
			_, _ = w.Write([]byte(`{"submissions":[{"id":"s1","stage":"needs_changes","note":"Menu is empty"}]}`))
		}
	}))
	defer srv.Close()
	key := "0123456789abcdef0123456789abcdef"
	p := &httpVenuePartner{base: srv.URL, key: key, client: srv.Client()}

	rev, err := p.Submit(context.Background(), validVenue())
	if err != nil || rev.ID != "s1" || rev.InviteLink == nil || *rev.InviteLink != "https://x.test/claim/abc" || gotKey != key || gotPath != "/api/heygo/venue-submissions" {
		t.Fatalf("submit: %+v %v key=%q path=%q", rev, err, gotKey, gotPath)
	}
	dup := validVenue()
	dup.VenueName = "Dup"
	if _, err := p.Submit(context.Background(), dup); err == nil {
		t.Fatal("expected conflict")
	} else if pe, ok := err.(*venuePartnerError); !ok || pe.Status != 409 || pe.Duplicate == nil || pe.Duplicate.DaysLeft != 4 {
		t.Fatalf("expected 409 partner error, got %v", err)
	}
	down := validVenue()
	down.VenueName = "Down"
	if _, err := p.Submit(context.Background(), down); err != errVenuePartnerUnavailable {
		t.Fatalf("expected unavailable, got %v", err)
	}
	d, err := p.Check(context.Background(), "hero-1", "Mama Put", 6.5, 3.3, true)
	if err != nil || d.State != "locked" || d.DaysLeft != 5 {
		t.Fatalf("check: %+v %v", d, err)
	}
	st, err := p.Statuses(context.Background(), "hero 1")
	if err != nil || st["s1"].Stage != "needs_changes" || *st["s1"].Note != "Menu is empty" {
		t.Fatalf("statuses: %+v %v", st, err)
	}
}

func TestDuplicateMessage(t *testing.T) {
	code, msg := duplicateMessage(&venueDuplicate{State: "locked", DaysAgo: 3, DaysLeft: 4})
	if code != "venue_locked" || msg != "Another Hero invited this venue 3 days ago. If the owner does not join within 4 days, you can list it." {
		t.Fatalf("locked: %s %s", code, msg)
	}
	if code, _ = duplicateMessage(&venueDuplicate{State: "on_heygo"}); code != "venue_on_heygo" {
		t.Fatalf("on heygo: %s", code)
	}
	if code, _ = duplicateMessage(&venueDuplicate{State: "yours"}); code != "venue_yours" {
		t.Fatalf("yours: %s", code)
	}
	if code, _ = duplicateMessage(nil); code != "venue_exists" {
		t.Fatalf("nil: %s", code)
	}
}

func TestMaskDestination(t *testing.T) {
	if got := maskDestination("phone", "+2348031234567"); got != "•••• 4567" {
		t.Fatalf("phone: %s", got)
	}
	if got := maskDestination("email", "ada@example.com"); got != "a***@example.com" {
		t.Fatalf("email: %s", got)
	}
}
