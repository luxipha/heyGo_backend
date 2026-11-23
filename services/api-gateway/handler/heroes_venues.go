package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/env"
	"github.com/luxipha/heyGo_backend/shared/httpmiddleware"
)

const (
	maxVenuePhotos         = 3
	maxVenueRequestBytes   = 8 << 20
	maxVenueSubmissionsDay = 10
	maxVenueGPSAccuracyM   = 300
)

// validateVenueSubmission normalises and checks a Hero's input. It returns a
// message safe to show the Hero, or "" when the input is acceptable.
func validateVenueSubmission(s *venueSubmission) string {
	// Confirmation is granted only after the server verifies the owner code.
	s.OwnerConfirmed = false
	s.VenueName = strings.TrimSpace(s.VenueName)
	s.Address = strings.TrimSpace(s.Address)
	s.City = strings.TrimSpace(s.City)
	s.OwnerName = strings.TrimSpace(s.OwnerName)
	s.Notes = strings.TrimSpace(s.Notes)
	if s.VenueName == "" || len(s.VenueName) > 120 {
		return "Enter the venue's name"
	}
	if s.Address == "" || len(s.Address) > 300 {
		return "Enter the venue's address"
	}
	phone, ok := normalizePhone(s.Phone)
	if !ok {
		return "Enter a valid venue phone number"
	}
	s.Phone = phone
	if s.OwnerName == "" || len(s.OwnerName) > 100 {
		return "Enter the owner's name"
	}
	if strings.TrimSpace(s.OwnerPhone) != "" {
		op, ok := normalizePhone(s.OwnerPhone)
		if !ok {
			return "Enter a valid owner phone number"
		}
		s.OwnerPhone = op
	}
	if strings.TrimSpace(s.OwnerEmail) != "" {
		email, ok := heroEmail(s.OwnerEmail)
		if !ok {
			return "Enter a valid owner email"
		}
		s.OwnerEmail = email
	}
	if s.OwnerPhone == "" && s.OwnerEmail == "" {
		return "Add the owner's phone number or email"
	}
	if s.OwnerRole != "owner" && s.OwnerRole != "manager" {
		s.OwnerRole = "owner"
	}
	s.Landmark = strings.TrimSpace(s.Landmark)
	if len(s.Landmark) > 160 {
		s.Landmark = s.Landmark[:160]
	}
	if !venueKitchenTypes[s.KitchenType] {
		return "Pick what kind of kitchen it is"
	}
	clean := make([]string, 0, len(s.Sells))
	for _, item := range s.Sells {
		if venueSells[item] {
			clean = append(clean, item)
		}
	}
	s.Sells = clean
	s.SocialHandle = strings.TrimSpace(s.SocialHandle)
	if len(s.SocialHandle) > 80 {
		s.SocialHandle = s.SocialHandle[:80]
	}
	if s.MenuPhoto != "" && !strings.HasPrefix(s.MenuPhoto, "data:image/") {
		return "The menu photo must be an image"
	}
	// The pin is how we know the Hero was at the venue, so it is required and must be reasonably exact.
	if s.Location == nil || s.Location.Lat < -90 || s.Location.Lat > 90 || s.Location.Lng < -180 || s.Location.Lng > 180 || (s.Location.Lat == 0 && s.Location.Lng == 0) {
		return "Turn on location and pin the venue where you are standing"
	}
	if s.LocationAccuracyM <= 0 || s.LocationAccuracyM > maxVenueGPSAccuracyM {
		return "Your location is not exact enough yet. Step outside or wait a moment and try again"
	}
	if len(s.Photos) < 1 || len(s.Photos) > maxVenuePhotos {
		return "Add 1 to 3 photos of the venue"
	}
	for _, p := range s.Photos {
		if !strings.HasPrefix(p, "data:image/") {
			return "Photos must be images"
		}
	}
	if len(s.Notes) > 1000 {
		s.Notes = s.Notes[:1000]
	}
	return ""
}

var (
	venueKitchenTypes = map[string]bool{"Restaurant": true, "Bukka": true, "Home kitchen": true, "Small chops": true}
	venueSells        = map[string]bool{"Rice": true, "Swallow": true, "Grills": true, "Shawarma": true, "Small chops": true, "Drinks": true}
)

// venueRequest is what the app sends: the venue itself plus bookkeeping that is not passed on.
type venueRequest struct {
	venueSubmission
	ClientRef           string `json:"clientRef"`
	OwnerConfirmationID string `json:"ownerConfirmationId"`
}

func registerHeroVenueRoutes(r *gin.Engine, a *heroesAPI, partner venuePartner, sms SMSSender) {
	if a == nil {
		return
	}
	// Registered outside the /heroes group: that group caps bodies at 16KB,
	// which is too small for photos.
	g := r.Group("/heroes/venues")
	g.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxVenueRequestBytes)
		c.Next()
	})
	g.Use(a.authenticate)
	g.POST("", func(c *gin.Context) { a.submitVenue(c, partner) })
	g.GET("", func(c *gin.Context) { a.listVenues(c, partner) })
	g.GET("/:id", func(c *gin.Context) { a.venueDetail(c) })
	g.POST("/:id/invite", func(c *gin.Context) { a.reinviteVenue(c, partner) })
	g.POST("/owner-code", httpmiddleware.NewRateLimiter(20, time.Minute).Middleware, func(c *gin.Context) { a.sendOwnerCode(c, sms) })
	g.POST("/owner-code/verify", httpmiddleware.NewRateLimiter(40, time.Minute).Middleware, a.verifyOwnerCode)
	g.POST("/check", httpmiddleware.NewRateLimiter(40, time.Minute).Middleware, func(c *gin.Context) { a.checkVenue(c, partner) })
}

// duplicateMessage turns the partner's verdict into words a Hero understands.
func duplicateMessage(d *venueDuplicate) (code, message string) {
	if d == nil {
		return "venue_exists", "This venue is already listed or under review"
	}
	switch d.State {
	case "locked":
		ago := "recently"
		if d.DaysAgo == 1 {
			ago = "1 day ago"
		} else if d.DaysAgo > 1 {
			ago = fmt.Sprintf("%d days ago", d.DaysAgo)
		}
		left := fmt.Sprintf("%d days", d.DaysLeft)
		if d.DaysLeft <= 1 {
			left = "a day"
		}
		return "venue_locked", fmt.Sprintf("Another Hero invited this venue %s. If the owner does not join within %s, you can list it.", ago, left)
	case "yours":
		return "venue_yours", "You already listed this venue."
	default:
		return "venue_on_heygo", "This venue is already on heyGo. Listing it again will not earn a bonus."
	}
}

func (a *heroesAPI) submitVenue(c *gin.Context, partner venuePartner) {
	if partner == nil {
		driverError(c, http.StatusServiceUnavailable, "venues_unavailable", "Listing venues is temporarily unavailable")
		return
	}
	var req venueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		driverError(c, http.StatusBadRequest, "invalid_venue", "Check the venue details and photos, then try again")
		return
	}
	s := req.venueSubmission
	if msg := validateVenueSubmission(&s); msg != "" {
		driverError(c, http.StatusBadRequest, "invalid_venue", msg)
		return
	}
	ctx := c.Request.Context()
	var heroName, heroMail string
	var recent int
	err := a.pool.QueryRow(ctx, `SELECT h.name,h.email,(SELECT count(*) FROM hero_venue_submissions v WHERE v.hero_id=h.id AND v.created_at>now()-interval '24 hours') FROM heroes_accounts h WHERE h.id=$1::uuid`, heroID(c)).Scan(&heroName, &heroMail, &recent)
	if err != nil {
		heroFailure(c)
		return
	}
	// A Hero must not register themselves as the venue's owner.
	if s.OwnerEmail != "" && strings.EqualFold(s.OwnerEmail, heroMail) {
		driverError(c, http.StatusBadRequest, "invalid_venue", "Use the owner's own email, not yours")
		return
	}
	if recent >= maxVenueSubmissionsDay {
		driverError(c, http.StatusTooManyRequests, "venue_limit", "Daily venue limit reached. Try again tomorrow.")
		return
	}
	// The owner's confirmation counts only if it was for this contact, is fresh and unused.
	confirmationID := ""
	if req.OwnerConfirmationID != "" {
		if !a.ownerConfirmationValid(ctx, heroID(c), req.OwnerConfirmationID, s.OwnerPhone, s.OwnerEmail) {
			driverError(c, http.StatusBadRequest, "owner_confirmation_invalid", "Confirm the owner again, or list the venue without confirming")
			return
		}
		confirmationID = req.OwnerConfirmationID
		s.OwnerConfirmed = true
	}
	s.HeroID = heroID(c)
	s.HeroName = heroName
	s.ExternalID = uuid.NewString()
	if _, err := uuid.Parse(req.ClientRef); err == nil {
		s.ExternalID = req.ClientRef
	}
	review, err := partner.Submit(ctx, s)
	var pe *venuePartnerError
	switch {
	case errors.As(err, &pe) && pe.Status == http.StatusConflict:
		code, message := duplicateMessage(pe.Duplicate)
		driverError(c, http.StatusConflict, code, message)
		return
	case errors.As(err, &pe):
		driverError(c, http.StatusBadRequest, "invalid_venue", pe.Message)
		return
	case err != nil:
		driverError(c, http.StatusServiceUnavailable, "venues_unavailable", "Could not send the venue right now. Please try again.")
		return
	}
	stage := review.Stage
	if stage == "" {
		stage = "waiting_for_owner"
	}
	_, err = a.pool.Exec(ctx, `INSERT INTO hero_venue_submissions(id,hero_id,partner_submission_id,venue_name,stage,area,owner_phone,owner_confirmation_id,owner_name)VALUES($1::uuid,$2::uuid,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),NULLIF($8,'')::uuid,NULLIF($9,''))ON CONFLICT(partner_submission_id)DO NOTHING`, uuid.NewString(), s.HeroID, review.ID, s.VenueName, stage, s.City, s.OwnerPhone, confirmationID, s.OwnerName)
	if err != nil {
		heroFailure(c)
		return
	}
	heroData(c, gin.H{"listed": true, "venueName": s.VenueName, "stage": stage, "inviteLink": review.InviteLink})
}

type heroVenueRow struct {
	ID              string  `json:"id"`
	VenueName       string  `json:"venueName"`
	Area            *string `json:"area"`
	Stage           string  `json:"stage"`
	Note            *string `json:"note"`
	OwnerPhone      *string `json:"ownerPhone"`
	OrdersDelivered int     `json:"ordersDelivered"`
	EarnedKobo      int64   `json:"earnedKobo"`
	PendingKobo     int64   `json:"pendingKobo"`
	BonusesPaid     bool    `json:"bonusesPaid"`
	// For live venues, the Earning card: when it went live, its last delivered order, and this
	// week's orders and earnings (Monday first, Lagos time) with the average earned per order.
	LiveAt       *time.Time `json:"liveAt"`
	LastOrderAt  *time.Time `json:"lastOrderAt"`
	WeekOrders   int        `json:"weekOrders"`
	WeekKobo     int64      `json:"weekKobo"`
	PerOrderKobo int64      `json:"perOrderKobo"`
	WeekDays     [7]int     `json:"weekDays"`
	ref          string
	CreatedAt    string `json:"createdAt"`
}

var validVenueStages = map[string]bool{"waiting_for_owner": true, "owner_joined": true, "in_review": true, "needs_changes": true, "live": true, "archived": true}

func (a *heroesAPI) listVenues(c *gin.Context, partner venuePartner) {
	ctx := c.Request.Context()
	hero := heroID(c)
	// Best effort: pull the latest progress, but still show the Hero's own history if the partner is down.
	if partner != nil {
		if statuses, err := partner.Statuses(ctx, hero); err == nil {
			for id, st := range statuses {
				if validVenueStages[st.Stage] {
					_, _ = a.pool.Exec(ctx, `UPDATE hero_venue_submissions SET stage=$1,note=$2,updated_at=now(),claimed_at=CASE WHEN claimed_at IS NULL AND $1 IN ('owner_joined','in_review','needs_changes','live') THEN now() ELSE claimed_at END,live_at=CASE WHEN live_at IS NULL AND $1='live' THEN now() ELSE live_at END WHERE partner_submission_id=$3 AND hero_id=$4::uuid AND (stage<>$1 OR note IS DISTINCT FROM $2)`, st.Stage, st.Note, id, hero)
				}
			}
		}
	}
	rows, err := a.pool.Query(ctx, `SELECT v.id::text,v.venue_name,v.area,v.stage,v.note,v.owner_phone,(SELECT count(*) FROM hero_venue_earnings e WHERE e.venue_ref=v.partner_submission_id AND e.kind='order_share' AND e.status<>'rejected'),COALESCE((SELECT sum(e.amount_kobo) FROM hero_venue_earnings e WHERE e.venue_ref=v.partner_submission_id AND e.status='approved'),0),COALESCE((SELECT sum(e.amount_kobo) FROM hero_venue_earnings e WHERE e.venue_ref=v.partner_submission_id AND e.status='pending'),0),(v.stage='live' AND ((SELECT live_bonus_kobo FROM heroes_settings WHERE id=1)=0 OR EXISTS(SELECT 1 FROM hero_venue_earnings e WHERE e.venue_ref=v.partner_submission_id AND e.kind='live_bonus' AND e.status='approved')) AND ((SELECT orders_bonus_kobo FROM heroes_settings WHERE id=1)=0 OR EXISTS(SELECT 1 FROM hero_venue_earnings e WHERE e.venue_ref=v.partner_submission_id AND e.kind='orders_bonus' AND e.status='approved'))),v.created_at,v.partner_submission_id,v.live_at,(SELECT max(e.delivered_at) FROM hero_venue_earnings e WHERE e.venue_ref=v.partner_submission_id AND e.kind='order_share' AND e.status<>'rejected') FROM hero_venue_submissions v WHERE v.hero_id=$1::uuid AND v.stage<>'archived' ORDER BY v.created_at DESC LIMIT 200`, hero)
	if err != nil {
		heroFailure(c)
		return
	}
	defer rows.Close()
	out := []heroVenueRow{}
	for rows.Next() {
		var v heroVenueRow
		var created time.Time
		if err := rows.Scan(&v.ID, &v.VenueName, &v.Area, &v.Stage, &v.Note, &v.OwnerPhone, &v.OrdersDelivered, &v.EarnedKobo, &v.PendingKobo, &v.BonusesPaid, &created, &v.ref, &v.LiveAt, &v.LastOrderAt); err != nil {
			heroFailure(c)
			return
		}
		v.CreatedAt = created.UTC().Format(time.RFC3339)
		out = append(out, v)
	}
	if rows.Err() != nil {
		heroFailure(c)
		return
	}
	rows.Close()
	// Live venues also carry this week's numbers, for the Earning card.
	for i := range out {
		if out[i].Stage != "live" {
			continue
		}
		var w heroVenueDetail
		if err := a.fillVenueWeeks(ctx, out[i].ref, &w); err != nil {
			heroFailure(c)
			return
		}
		out[i].WeekOrders = w.ThisWeek.Orders
		out[i].WeekKobo = w.ThisWeek.Kobo
		out[i].PerOrderKobo = w.PerOrder
		for d := range w.ThisWeek.Days {
			out[i].WeekDays[d] = w.ThisWeek.Days[d].Orders
		}
	}
	heroData(c, gin.H{"venues": out})
}

// reinviteVenue issues a fresh invite link for a venue the owner has not claimed yet.
func (a *heroesAPI) reinviteVenue(c *gin.Context, partner venuePartner) {
	if partner == nil {
		driverError(c, http.StatusServiceUnavailable, "venues_unavailable", "Listing venues is temporarily unavailable")
		return
	}
	var partnerID string
	err := a.pool.QueryRow(c.Request.Context(), `SELECT partner_submission_id FROM hero_venue_submissions WHERE id=$1::uuid AND hero_id=$2::uuid`, c.Param("id"), heroID(c)).Scan(&partnerID)
	if err != nil {
		driverError(c, http.StatusNotFound, "venue_not_found", "Venue not found")
		return
	}
	review, err := partner.Reinvite(c.Request.Context(), heroID(c), partnerID)
	var pe *venuePartnerError
	switch {
	case errors.As(err, &pe):
		driverError(c, pe.Status, "invite_unavailable", pe.Message)
		return
	case err != nil:
		driverError(c, http.StatusServiceUnavailable, "venues_unavailable", "Could not create a new invite right now. Please try again.")
		return
	}
	heroData(c, gin.H{"inviteLink": review.InviteLink})
}

// checkVenue tells the form whether a venue like this can be listed. It never says who
// listed it, only whether it is already on heyGo, held by another Hero, or this Hero's own.
func (a *heroesAPI) checkVenue(c *gin.Context, partner venuePartner) {
	unchecked := func() { c.JSON(http.StatusOK, gin.H{"data": gin.H{"checked": false}}) }
	if partner == nil {
		unchecked()
		return
	}
	var b struct {
		VenueName string `json:"venueName"`
		Location  *struct {
			Lat float64 `json:"lat"`
			Lng float64 `json:"lng"`
		} `json:"location"`
	}
	if c.ShouldBindJSON(&b) != nil || len(strings.TrimSpace(b.VenueName)) < 3 || len(b.VenueName) > 120 {
		unchecked()
		return
	}
	var lat, lng float64
	has := b.Location != nil
	if has {
		lat, lng = b.Location.Lat, b.Location.Lng
	}
	d, err := partner.Check(c.Request.Context(), heroID(c), strings.TrimSpace(b.VenueName), lat, lng, has)
	if err != nil {
		// Never block listing because the check is unavailable.
		unchecked()
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"checked": true, "state": d.State, "daysAgo": d.DaysAgo, "daysLeft": d.DaysLeft}})
}

// How long a listed venue stays with the Hero who listed it while its owner has not joined.
func venueLockDays() int {
	n := env.GetInt("VENUE_LISTING_LOCK_DAYS", 7)
	if n < 1 {
		return 7
	}
	return n
}

type venueBonus struct {
	AmountKobo int64      `json:"amountKobo"`
	Status     string     `json:"status"`
	At         *time.Time `json:"at"`
}

type heroVenueDetail struct {
	ID              string      `json:"id"`
	VenueName       string      `json:"venueName"`
	Area            *string     `json:"area"`
	Stage           string      `json:"stage"`
	Note            *string     `json:"note"`
	OwnerName       *string     `json:"ownerName"`
	OwnerPhone      *string     `json:"ownerPhone"`
	OwnerConfirmed  bool        `json:"ownerConfirmed"`
	ListedAt        time.Time   `json:"listedAt"`
	ClaimedAt       *time.Time  `json:"claimedAt"`
	LiveAt          *time.Time  `json:"liveAt"`
	LockDaysLeft    *int        `json:"lockDaysLeft"`
	OrdersDelivered int         `json:"ordersDelivered"`
	LastOrderAt     *time.Time  `json:"lastOrderAt"`
	OrderShareKobo  int64       `json:"orderShareKobo"`
	OrderSharePend  int64       `json:"orderSharePendingKobo"`
	LiveBonus       *venueBonus `json:"liveBonus"`
	OrdersBonus     *venueBonus `json:"ordersBonus"`
	// What the venue's orders earned, for the Earning view: this week and last week by day
	// (Monday first, Lagos time), the last six weeks, and the all-time total.
	ThisWeek venueWeek    `json:"thisWeek"`
	LastWeek venueWeek    `json:"lastWeek"`
	Weeks    []venueTotal `json:"weeks"`
	AllTime  venueTotal   `json:"allTime"`
	PerOrder int64        `json:"perOrderKobo"`
}

type venueDay struct {
	Orders int   `json:"orders"`
	Kobo   int64 `json:"kobo"`
}

type venueWeek struct {
	Orders int         `json:"orders"`
	Kobo   int64       `json:"kobo"`
	Days   [7]venueDay `json:"days"`
}

type venueTotal struct {
	Start  string `json:"start,omitempty"`
	Orders int    `json:"orders"`
	Kobo   int64  `json:"kobo"`
}

// venueDetail is everything the Hero needs to see about one of their own venues: where it
// is stuck (and for how long they keep it), or, once live, what it has earned them.
func (a *heroesAPI) venueDetail(c *gin.Context) {
	if _, err := uuid.Parse(c.Param("id")); err != nil {
		driverError(c, http.StatusNotFound, "venue_not_found", "Venue not found")
		return
	}
	ctx := c.Request.Context()
	var v heroVenueDetail
	var ref string
	var confirmed bool
	err := a.pool.QueryRow(ctx, `SELECT s.id::text,s.partner_submission_id,s.venue_name,s.area,s.stage,s.note,s.owner_name,s.owner_phone,s.owner_confirmation_id IS NOT NULL,s.created_at,s.claimed_at,s.live_at FROM hero_venue_submissions s WHERE s.id=$1::uuid AND s.hero_id=$2::uuid AND s.stage<>'archived'`, c.Param("id"), heroID(c)).
		Scan(&v.ID, &ref, &v.VenueName, &v.Area, &v.Stage, &v.Note, &v.OwnerName, &v.OwnerPhone, &confirmed, &v.ListedAt, &v.ClaimedAt, &v.LiveAt)
	if err != nil {
		driverError(c, http.StatusNotFound, "venue_not_found", "Venue not found")
		return
	}
	v.OwnerConfirmed = confirmed
	if v.Stage == "waiting_for_owner" {
		left := venueLockDays() - int(time.Since(v.ListedAt).Hours()/24)
		if left < 0 {
			left = 0
		}
		v.LockDaysLeft = &left
	}
	err = a.pool.QueryRow(ctx, `SELECT
 count(*) FILTER(WHERE kind='order_share' AND status<>'rejected'),
 max(delivered_at) FILTER(WHERE kind='order_share' AND status<>'rejected'),
 COALESCE(sum(amount_kobo) FILTER(WHERE kind='order_share' AND status='approved'),0),
 COALESCE(sum(amount_kobo) FILTER(WHERE kind='order_share' AND status='pending'),0)
 FROM hero_venue_earnings WHERE venue_ref=$1 AND hero_id=$2::uuid`, ref, heroID(c)).Scan(&v.OrdersDelivered, &v.LastOrderAt, &v.OrderShareKobo, &v.OrderSharePend)
	if err != nil {
		heroFailure(c)
		return
	}
	rows, err := a.pool.Query(ctx, `SELECT kind,amount_kobo,status,reviewed_at FROM hero_venue_earnings WHERE venue_ref=$1 AND hero_id=$2::uuid AND kind IN ('live_bonus','orders_bonus')`, ref, heroID(c))
	if err != nil {
		heroFailure(c)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var kind, status string
		var amount int64
		var at *time.Time
		if rows.Scan(&kind, &amount, &status, &at) != nil {
			heroFailure(c)
			return
		}
		b := &venueBonus{AmountKobo: amount, Status: status, At: at}
		if kind == "live_bonus" {
			v.LiveBonus = b
		} else {
			v.OrdersBonus = b
		}
	}
	if rows.Err() != nil {
		heroFailure(c)
		return
	}
	if err := a.fillVenueWeeks(ctx, ref, &v); err != nil {
		heroFailure(c)
		return
	}
	heroData(c, v)
}

// fillVenueWeeks groups the venue's delivered orders by Lagos week and weekday. Orders that
// are still waiting for approval count (they are earned), rejected ones do not.
func (a *heroesAPI) fillVenueWeeks(ctx context.Context, ref string, v *heroVenueDetail) error {
	var thisMonday time.Time
	if err := a.pool.QueryRow(ctx, `SELECT date_trunc('week', now() AT TIME ZONE 'Africa/Lagos')::date`).Scan(&thisMonday); err != nil {
		return err
	}
	rows, err := a.pool.Query(ctx, `SELECT date_trunc('week', delivered_at AT TIME ZONE 'Africa/Lagos')::date,extract(isodow FROM delivered_at AT TIME ZONE 'Africa/Lagos')::int,count(*),COALESCE(sum(amount_kobo),0)
FROM hero_venue_earnings WHERE venue_ref=$1 AND kind='order_share' AND status<>'rejected' AND delivered_at IS NOT NULL
GROUP BY 1,2`, ref)
	if err != nil {
		return err
	}
	defer rows.Close()
	const weeksShown = 6
	v.Weeks = make([]venueTotal, weeksShown)
	for i := 0; i < weeksShown; i++ {
		v.Weeks[i].Start = thisMonday.AddDate(0, 0, -7*(weeksShown-1-i)).Format("2006-01-02")
	}
	for rows.Next() {
		var monday time.Time
		var dow, n int
		var kobo int64
		if err := rows.Scan(&monday, &dow, &n, &kobo); err != nil {
			return err
		}
		v.AllTime.Orders += n
		v.AllTime.Kobo += kobo
		ago := int(thisMonday.Sub(monday).Hours() / 24 / 7)
		if dow < 1 || dow > 7 {
			continue
		}
		switch ago {
		case 0:
			v.ThisWeek.Days[dow-1] = venueDay{Orders: n, Kobo: kobo}
			v.ThisWeek.Orders += n
			v.ThisWeek.Kobo += kobo
		case 1:
			v.LastWeek.Days[dow-1] = venueDay{Orders: n, Kobo: kobo}
			v.LastWeek.Orders += n
			v.LastWeek.Kobo += kobo
		}
		if ago >= 0 && ago < weeksShown {
			w := &v.Weeks[weeksShown-1-ago]
			w.Orders += n
			w.Kobo += kobo
		}
	}
	if v.AllTime.Orders > 0 {
		v.PerOrder = v.AllTime.Kobo / int64(v.AllTime.Orders)
	}
	return rows.Err()
}
