package handler

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeDriverFeedbackTags(t *testing.T) {
	empty, err := normalizeDriverFeedbackTags(nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty tags = %#v, %v", empty, err)
	}
	got, err := normalizeDriverFeedbackTags([]string{"easy_pickup", "great_chat"})
	if err != nil || !reflect.DeepEqual(got, []string{"easy_pickup", "great_chat"}) {
		t.Fatalf("normalize tags = %v, %v", got, err)
	}
	for _, tags := range [][]string{{"unknown"}, {"great_chat", "great_chat"}, {"clean_and_tidy", "easy_pickup", "great_chat", "extra"}} {
		if _, err := normalizeDriverFeedbackTags(tags); err == nil {
			t.Fatalf("expected invalid tags to fail: %v", tags)
		}
	}
}

func TestNormalizeRiderRatingComment(t *testing.T) {
	comment, err := normalizeRiderRatingComment("  Helpful rider.  ")
	if err != nil || comment != "Helpful rider." {
		t.Fatalf("normalized comment = %q, %v", comment, err)
	}
	if comment, err := normalizeRiderRatingComment("   "); err != nil || comment != "" {
		t.Fatalf("optional empty comment = %q, %v", comment, err)
	}
	if _, err := normalizeRiderRatingComment(strings.Repeat("a", 251)); err == nil {
		t.Fatal("expected a 251-character comment to fail")
	}
	if _, err := normalizeRiderRatingComment(strings.Repeat("界", 250)); err != nil {
		t.Fatalf("250 Unicode characters should be accepted: %v", err)
	}
}
