package adminauth

import "testing"

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("a-long-staff-password")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "a-long-staff-password") {
		t.Fatal("correct password rejected")
	}
	if VerifyPassword(hash, "wrong-password") {
		t.Fatal("wrong password accepted")
	}
	if VerifyPassword("$argon2id$v=19$m=99999999,t=2,p=1$bad$bad", "a-long-staff-password") {
		t.Fatal("unbounded hash parameters accepted")
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("weak password accepted")
	}
}
