package handler

import (
	"regexp"
	"strings"
)

// vinPattern matches a normalised VIN / chassis number. Modern VINs are 17
// characters; older and imported chassis numbers vary, so 5–30 is accepted.
var vinPattern = regexp.MustCompile(`^[A-Z0-9]{5,30}$`)

// normalizeVIN uppercases and drops the spaces, hyphens and dots staff often
// copy from plates, so the same chassis always stores the same value.
func normalizeVIN(raw string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '.', '\t':
			return -1
		}
		return r
	}, strings.ToUpper(strings.TrimSpace(raw)))
}
