package handler

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

var allowedDriverFeedbackTags = map[string]struct{}{
	"clean_and_tidy": {},
	"easy_pickup":    {},
	"great_chat":     {},
}

func normalizeRiderRatingComment(comment string) (string, error) {
	comment = strings.TrimSpace(comment)
	if !utf8.ValidString(comment) {
		return "", fmt.Errorf("comment must be valid UTF-8")
	}
	if utf8.RuneCountInString(comment) > 250 {
		return "", fmt.Errorf("comment must be 250 characters or fewer")
	}
	return comment, nil
}

func normalizeDriverFeedbackTags(tags []string) ([]string, error) {
	if tags == nil {
		tags = []string{}
	}
	if len(tags) > len(allowedDriverFeedbackTags) {
		return nil, fmt.Errorf("choose at most %d feedback tags", len(allowedDriverFeedbackTags))
	}
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		if _, ok := allowedDriverFeedbackTags[tag]; !ok {
			return nil, fmt.Errorf("feedback tag %q is not supported", tag)
		}
		if _, ok := seen[tag]; ok {
			return nil, fmt.Errorf("feedback tags must be unique")
		}
		seen[tag] = struct{}{}
	}
	return tags, nil
}
