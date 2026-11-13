package handler

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// Existing challenge rows are the durable send budget. Never exclude consumed
// rows: consumption, verification failure, or uncertain delivery cannot refund it.
const otpBudgetSQL = `WITH sends AS (
 SELECT channel,destination,pin_reset_user_id AS owner,created_at FROM auth_otp_challenges
 WHERE created_at>NOW()-INTERVAL '24 hours'
 UNION ALL SELECT 'phone',destination,driver_id,created_at FROM driver_phone_challenges
 WHERE created_at>NOW()-INTERVAL '24 hours'
 UNION ALL SELECT 'phone',destination,rider_id,created_at FROM rider_phone_challenges
 WHERE created_at>NOW()-INTERVAL '24 hours'
 UNION ALL SELECT CASE WHEN purpose='phone_change' THEN 'phone' ELSE 'email' END,new_destination,rider_id,created_at
 FROM rider_contact_challenges WHERE created_at>NOW()-INTERVAL '24 hours'
 UNION ALL SELECT 'phone',current_destination,rider_id,created_at FROM rider_contact_challenges
 WHERE purpose='phone_change' AND created_at>NOW()-INTERVAL '24 hours'
 ) SELECT COUNT(*) FILTER(WHERE created_at>NOW()-INTERVAL '10 minutes'),
 COUNT(*) FILTER(WHERE created_at>NOW()-INTERVAL '1 hour'),COUNT(*),
 COALESCE(EXTRACT(EPOCH FROM NOW()-MAX(created_at)),86400)::float8
 FROM sends WHERE channel=$1 AND (($2<>'' AND destination=$2) OR ($3<>'' AND owner=NULLIF($3,'')::uuid))`

func otpBudgetAllowance(ctx context.Context, q otpQuerier, channel, destination, owner string, cost int) (bool, int, string, error) {
	var tenMinutes, hour, day int
	var sinceLast float64
	if err := q.QueryRow(ctx, otpBudgetSQL, channel, destination, owner).Scan(&tenMinutes, &hour, &day, &sinceLast); err != nil {
		return false, 0, "", err
	}
	where := "text messages"
	if channel == "email" {
		where = "email"
	}
	// Check long windows first so a day-limited client is not told to retry in a minute.
	switch {
	case day+cost > otpRequestsPerDay:
		return false, otpRetryAfterDay, "Too many codes requested today. Try again tomorrow or contact Support.", nil
	case hour+cost > otpRequestsPerHour:
		return false, otpRetryAfterHour, "Too many codes requested. Check your " + where + " for the latest code, or try again in an hour.", nil
	case tenMinutes+cost > otpRequestsPerTenMinutes:
		return false, otpRetryAfterWindow, "Please wait before requesting another code. Check your " + where + " for the latest one.", nil
	case sinceLast < otpResendCooldown.Seconds():
		wait := int(otpResendCooldown.Seconds()-sinceLast) + 1
		return false, wait, fmt.Sprintf("A code was just requested. Check your %s, or try again in %d seconds.", where, wait), nil
	}
	return true, 0, "", nil
}

// Account and destination advisory locks share one sorted order. Authenticated
// callers already hold their user row lock; this helper never takes user locks.
// This also serializes sign-in, recovery and linking against each other.
func reserveOTPBudget(c *gin.Context, tx pgx.Tx, channel, owner string, destinations ...string) bool {
	keys := make([]string, 0, len(destinations)+1)
	if owner != "" {
		keys = append(keys, "otp-owner:"+channel+":"+owner)
	}
	unique := map[string]bool{}
	for _, d := range destinations {
		if !unique[d] {
			unique[d] = true
			keys = append(keys, channel+":"+d)
		}
	}
	// Sorting all keys establishes one lock order even for two-destination changes.
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
			driverError(c, 503, "authentication_unavailable", "Verification is temporarily unavailable")
			return false
		}
	}
	check := func(destination, account string, cost int) bool {
		allowed, retry, message, err := otpBudgetAllowance(c.Request.Context(), tx, channel, destination, account, cost)
		if err != nil {
			driverError(c, 503, "authentication_unavailable", "Verification is temporarily unavailable")
			return false
		}
		if !allowed {
			c.Header("Retry-After", strconv.Itoa(retry))
			driverError(c, 429, "otp_rate_limited", message)
			return false
		}
		return true
	}
	for d := range unique {
		if !check(d, "", 1) {
			return false
		}
	}
	if owner != "" && !check("", owner, len(unique)) {
		return false
	}
	return true
}
