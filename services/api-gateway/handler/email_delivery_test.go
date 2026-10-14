package handler

import (
	"errors"
	"reflect"
	"testing"
)

func TestEmailProviderRouting(t *testing.T) {
	for _, tt := range []struct {
		name, primary          string
		fallback, resend, smtp bool
		first                  error
		want                   []string
		wantError              bool
	}{
		{"default", "resend", false, true, false, nil, []string{"resend"}, false},
		{"disabled primary fallback", "resend", true, false, true, nil, []string{"smtp"}, false},
		{"definite rejection fallback", "resend", true, true, true, errEmailUnavailable, []string{"resend", "smtp"}, false},
		{"unknown acceptance no duplicate", "resend", true, true, true, errors.New("timeout"), []string{"resend"}, true},
		{"fallback off", "resend", false, true, true, errEmailUnavailable, []string{"resend"}, true},
		{"smtp primary", "smtp", true, true, true, errEmailUnavailable, []string{"smtp", "resend"}, false},
		{"both disabled", "resend", true, false, false, nil, nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var called []string
			err := deliverEmail(tt.primary, tt.fallback, tt.resend, tt.smtp, func(p string) error {
				called = append(called, p)
				if len(called) == 1 && p == tt.primary {
					return tt.first
				}
				return nil
			})
			if !reflect.DeepEqual(called, tt.want) || (err != nil) != tt.wantError {
				t.Fatalf("calls=%v err=%v", called, err)
			}
		})
	}
}
func TestSMTPValidationAndSecretPurpose(t *testing.T) {
	s := emailDeliverySettings{PrimaryProvider: "resend", SMTPEnabled: true, Host: "smtp.example.test", Port: 587, TLSMode: "starttls", FromEmail: "otp@example.test"}
	if !validSMTPSettings(s) {
		t.Fatal("valid settings rejected")
	}
	for _, mutate := range []func(*emailDeliverySettings){func(s *emailDeliverySettings) { s.TLSMode = "none" }, func(s *emailDeliverySettings) { s.Host = "host\r\n" }, func(s *emailDeliverySettings) { s.FromName = "HeyGo\r\nBcc: victim" }, func(s *emailDeliverySettings) { s.Port = 0 }, func(s *emailDeliverySettings) { s.FromEmail = "invalid" }} {
		bad := s
		mutate(&bad)
		if validSMTPSettings(bad) {
			t.Fatal("unsafe settings accepted")
		}
	}
	key := []byte("01234567890123456789012345678901")
	sealed, err := sealAdminValue(key, "smtp-secret", "heygo-smtp-password-v1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = openAdminSecret(key, sealed); err == nil {
		t.Fatal("SMTP password accepted as Resend key")
	}
}
