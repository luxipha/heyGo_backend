package handler

import (
	"strings"
	"testing"
)

func TestPickupNoteValidation(t *testing.T) {
	for _, raw := range []string{"", "Meet at gate", "Line one\nLine two", strings.Repeat("é", 500)} {
		if _, ok := normalizePickupNote(raw); !ok {
			t.Fatal("valid note rejected")
		}
	}
	for _, raw := range []string{"bad\x00note", strings.Repeat("a", 501)} {
		if _, ok := normalizePickupNote(raw); ok {
			t.Fatal("invalid note accepted")
		}
	}
	if note, ok := normalizePickupNote("  Blue gate  "); !ok || note != "Blue gate" {
		t.Fatal("note not normalized")
	}
}
