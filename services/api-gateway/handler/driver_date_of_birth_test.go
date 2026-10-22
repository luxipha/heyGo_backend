package handler

import (
	"testing"
	"time"
)

func TestIdentityDateOfBirthValidation(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name, value string
		valid       bool
	}{
		{"adult", "1995-04-12", true},
		{"impossible date", "2000-02-30", false},
		{"future", now.AddDate(1, 0, 0).Format("2006-01-02"), false},
		{"under eighteen", now.AddDate(-17, 0, 0).Format("2006-01-02"), false},
		{"eighteenth birthday", now.AddDate(-18, 0, 0).Format("2006-01-02"), true},
		{"over maximum", now.AddDate(-121, 0, 0).Format("2006-01-02"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldAge := 99
			p := identityProfile{DateOfBirth: tc.value, Age: &oldAge}
			if got := normalizeIdentityProfile(&p); got != tc.valid {
				t.Fatalf("valid=%v want %v", got, tc.valid)
			}
			if tc.valid && (p.Age == nil || *p.Age == 99) {
				t.Fatal("age must derive from birth date")
			}
		})
	}
}
