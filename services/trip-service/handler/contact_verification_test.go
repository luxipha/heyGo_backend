package handler

import (
	"errors"
	"fmt"
	"github.com/luxipha/heyGo_backend/services/trip-service/repo"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestRiderContactVerificationErrorIsPublic(t *testing.T) {
	err := fmt.Errorf("create booking: %w", repo.ErrRiderContactUnverified)
	if !errors.Is(err, repo.ErrRiderContactUnverified) {
		t.Fatal("lost cause")
	}
	s := status.Convert(riderTripError(err))
	if s.Code() != codes.PermissionDenied {
		t.Fatal(s)
	}
	for _, d := range s.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.Domain == "heygo.rider" && info.Reason == "rider_contact_verification_required" {
			return
		}
	}
	t.Fatal("missing stable contact error", s)
}
