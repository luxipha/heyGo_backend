package handler

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

func TestOTPRequestAllowance(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: dsn, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = shareddb.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	seed := func(dest string, ages ...time.Duration) {
		t.Helper()
		for _, age := range ages {
			if _, err := pool.Exec(ctx, `INSERT INTO auth_otp_challenges(id,channel,destination,code_hash,expires_at,created_at) VALUES($1::uuid,'phone',$2,'x',NOW()+INTERVAL '5 minutes',NOW()-$3::interval)`, uuid.NewString(), dest, age.String()); err != nil {
				t.Fatal(err)
			}
		}
	}
	number := func() string { return "+234" + strings.ReplaceAll(uuid.NewString(), "-", "")[:10] }
	min := time.Minute

	cases := []struct {
		name       string
		ages       []time.Duration
		allowed    bool
		retryAfter func(int) bool
	}{
		{"first request is allowed", nil, true, nil},
		{"second request inside the cooldown is blocked", []time.Duration{20 * time.Second}, false, func(s int) bool { return s >= 1 && s <= 41 }},
		{"a request after the cooldown is allowed", []time.Duration{2 * min}, true, nil},
		{"three in ten minutes is blocked", []time.Duration{2 * min, 4 * min, 6 * min}, false, func(s int) bool { return s == otpRetryAfterWindow }},
		{"five in an hour is blocked", []time.Duration{11 * min, 20 * min, 30 * min, 40 * min, 50 * min}, false, func(s int) bool { return s == otpRetryAfterHour }},
		{"ten in a day is blocked", []time.Duration{70 * min, 2 * time.Hour, 4 * time.Hour, 6 * time.Hour, 8 * time.Hour, 10 * time.Hour, 12 * time.Hour, 14 * time.Hour, 16 * time.Hour, 18 * time.Hour}, false, func(s int) bool { return s == otpRetryAfterDay }},
		{"older than a day does not count", []time.Duration{25 * time.Hour, 26 * time.Hour, 27 * time.Hour, 28 * time.Hour, 29 * time.Hour, 30 * time.Hour, 31 * time.Hour, 32 * time.Hour, 33 * time.Hour, 34 * time.Hour}, true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dest := number()
			seed(dest, tc.ages...)
			allowed, retryAfter, message, err := otpRequestAllowance(ctx, pool, "phone", dest)
			if err != nil {
				t.Fatal(err)
			}
			if allowed != tc.allowed {
				t.Fatalf("allowed = %v (%q), want %v", allowed, message, tc.allowed)
			}
			if !allowed && (message == "" || tc.retryAfter == nil || !tc.retryAfter(retryAfter)) {
				t.Fatalf("retryAfter = %d, message = %q", retryAfter, message)
			}
		})
	}

	t.Run("another destination is unaffected", func(t *testing.T) {
		busy, other := number(), number()
		seed(busy, 10*time.Second)
		if allowed, _, _, err := otpRequestAllowance(ctx, pool, "phone", other); err != nil || !allowed {
			t.Fatalf("allowed = %v, err = %v", allowed, err)
		}
	})

	t.Run("channels are counted separately", func(t *testing.T) {
		dest := number()
		seed(dest, 10*time.Second)
		if allowed, _, _, err := otpRequestAllowance(ctx, pool, "email", dest); err != nil || !allowed {
			t.Fatalf("allowed = %v, err = %v", allowed, err)
		}
	})
}
