package geo

import (
	"context"
	"errors"
	"testing"
)

type usageStub struct {
	begun, finished int
	success         bool
	beginErr        error
	day             string
}

func (s *usageStub) Begin(context.Context, string) (string, error) {
	s.begun++
	return "2026-10-31", s.beginErr
}
func (s *usageStub) Finish(_ context.Context, _, day string, ok bool) error {
	s.finished++
	s.success = ok
	s.day = day
	return nil
}

type routerStub struct {
	calls int
	err   error
}

func (s *routerStub) Route(context.Context, Coordinate, Coordinate) (*RouteResponse, error) {
	s.calls++
	return &RouteResponse{}, s.err
}
func TestRouteUsageMeasurement(t *testing.T) {
	for _, failed := range []bool{false, true} {
		r := &routerStub{}
		if failed {
			r.err = errors.New("provider failed")
		}
		s := &usageStub{}
		_, err := (meteredRoutes{Router: r, Recorder: s, Provider: "google"}).Route(context.Background(), Coordinate{}, Coordinate{Latitude: 1})
		if (err != nil) != failed || r.calls != 1 || s.begun != 1 || s.finished != 1 || s.success == failed || s.day != "2026-10-31" {
			t.Fatalf("incorrect measurement %+v %+v %v", r, s, err)
		}
	}
}
func TestNoUntrackedPaidRequest(t *testing.T) {
	r := &routerStub{}
	s := &usageStub{beginErr: errors.New("db down")}
	if _, err := (meteredRoutes{Router: r, Recorder: s, Provider: "google"}).Route(context.Background(), Coordinate{}, Coordinate{}); err == nil || r.calls != 0 {
		t.Fatal("untracked provider request")
	}
}
func TestUsageAllowances(t *testing.T) {
	for _, tc := range []struct {
		provider                  string
		attempts, remaining, over int64
		warning                   bool
	}{{"google", 3999, 1001, 0, false}, {"google", 4000, 1000, 0, true}, {"google", 5001, 0, 1, true}, {"amap", 10001, 0, 1, true}, {"osrm", 100000, 0, 0, false}} {
		r := summarizeUsage(RouteUsage{Provider: tc.provider, Attempts: tc.attempts, Successes: 1})
		if r.Remaining != tc.remaining || r.OverAllowance != tc.over || r.Warning != tc.warning || r.Pending != tc.attempts-1 {
			t.Fatalf("bad allowance %+v", r)
		}
	}
}
