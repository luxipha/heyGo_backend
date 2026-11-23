package simulation

import (
	"fmt"
	"regexp"
	"testing"
)

func TestSyntheticPhoneScopeAndFormat(t *testing.T) {
	seen := map[string]bool{}
	format := regexp.MustCompile(`^\+000[0-9]{12}$`)
	for run := 0; run < 20; run++ {
		for participant := 0; participant < 100; participant++ {
			actor := identity(fmt.Sprintf("run-%d", run), fmt.Sprintf("participant-%d", participant))
			phone := syntheticPhone(actor)
			if phone != syntheticPhone(actor) {
				t.Fatal("contact is not deterministic")
			}
			if !format.MatchString(phone) {
				t.Fatalf("contact lost nondialable fixture prefix or numeric length: %q", phone)
			}
			if seen[phone] {
				t.Fatal("contact collision across fixture runs/participants")
			}
			seen[phone] = true
		}
	}
}
