package handler

import (
	"strings"
	"unicode/utf8"
)

type scheduledCancelReason struct {
	Code string `json:"reasonCode"`
	Note string `json:"note"`
}

func validScheduledCancelReason(r scheduledCancelReason) bool {
	if !utf8.ValidString(r.Note) || utf8.RuneCountInString(strings.TrimSpace(r.Note)) > 500 {
		return false
	}
	switch r.Code {
	case "plans_changed", "wrong_pickup_time", "wrong_route", "booked_by_mistake", "other":
		return true
	}
	return false
}
