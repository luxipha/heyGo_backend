package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/bugreports"
	"github.com/luxipha/heyGo_backend/shared/bugreports/mantis"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/httpmiddleware"
)

type bugReportAPI struct {
	pool     *pgxpool.Pool
	provider bugreports.Provider
}

func newBugReportAPI(pool *pgxpool.Pool) *bugReportAPI {
	a := &bugReportAPI{pool: pool}
	if os.Getenv("BUG_REPORT_PROVIDER") == "mantis" {
		project, _ := strconv.Atoi(os.Getenv("MANTIS_RIDER_PROJECT_ID"))
		category, _ := strconv.Atoi(os.Getenv("MANTIS_RIDER_CATEGORY_ID"))
		var fields map[int]string
		if raw := os.Getenv("MANTIS_RIDER_CUSTOM_FIELDS_JSON"); raw != "" {
			if json.Unmarshal([]byte(raw), &fields) != nil {
				return a
			}
		}
		p, err := mantis.New(mantis.Config{URL: os.Getenv("MANTIS_URL"), Token: os.Getenv("MANTIS_API_TOKEN"), ProjectID: project, CategoryID: category, CustomFields: fields})
		if err == nil {
			a.provider = p
		}
	}
	return a
}
func registerBugReportRoutes(g *gin.RouterGroup, pool *pgxpool.Pool) {
	a := newBugReportAPI(pool)
	g.GET("/bug-reports", a.list)
	g.POST("/bug-reports", httpmiddleware.NewRateLimiter(5, time.Minute).Middleware, a.create)
	g.GET("/bug-reports/:reportID", a.detail)
}
func registerAdminBugReportRoutes(g *gin.RouterGroup, pool *pgxpool.Pool, csrf gin.HandlerFunc) {
	a := newBugReportAPI(pool)
	g.GET("/support/bug-reports", a.list)
	g.GET("/support/bug-reports/:reportID", a.detail)
	g.POST("/support/bug-reports/:reportID/comments", csrf, a.comment)
	g.PATCH("/support/bug-reports/:reportID", csrf, a.setStatus)
}
func (a *bugReportAPI) available(c *gin.Context) bool {
	if a.provider == nil || a.pool == nil {
		driverError(c, 503, "bug_reporting_unavailable", "Bug reporting is currently unavailable")
		return false
	}
	return true
}

// Admin permission checks run on the /admin/support route group. App ownership
// is checked independently on every list/detail request; provider IDs are private.
func bugOwner(c *gin.Context) (string, bool) {
	if strings.HasPrefix(c.FullPath(), "/admin/") {
		if currentAdmin(c).ID == "" {
			driverError(c, 401, "authentication_required", "Authentication is required")
			return "", false
		}
		return "", true
	}
	u, ok := gatewayauth.CurrentUser(c)
	if !ok {
		driverError(c, 401, "authentication_required", "Authentication is required")
		return "", false
	}
	if !u.HasRole("rider") && !u.HasRole("driver") {
		driverError(c, 403, "app_account_required", "A rider or driver account is required")
		return "", false
	}
	return u.ID, true
}

type bugLink struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"createdAt"`
	Delivery   string    `json:"delivery"`
	ExternalID string    `json:"-"`
}

func (a *bugReportAPI) list(c *gin.Context) {
	owner, ok := bugOwner(c)
	if !ok || !a.available(c) {
		return
	}
	page, _ := strconv.Atoi(c.Query("page"))
	if page < 1 {
		page = 1
	}
	if page > 10000 {
		driverError(c, 400, "invalid_page", "Page is too large")
		return
	}
	rows, err := a.pool.Query(c.Request.Context(), `SELECT id::TEXT,created_at,COALESCE(external_id,'') FROM bug_report_links WHERE provider=$1 AND ($2='' OR owner_id::TEXT=$2) ORDER BY created_at DESC,id DESC LIMIT 21 OFFSET $3`, a.provider.Name(), owner, (page-1)*20)
	if err != nil {
		a.unavailable(c)
		return
	}
	defer rows.Close()
	items := []bugLink{}
	for rows.Next() {
		var l bugLink
		if rows.Scan(&l.ID, &l.CreatedAt, &l.ExternalID) != nil {
			a.unavailable(c)
			return
		}
		l.Delivery = "saved"
		if l.ExternalID == "" {
			l.Delivery = "unconfirmed"
		}
		items = append(items, l)
	}
	if rows.Err() != nil {
		a.unavailable(c)
		return
	}
	more := len(items) > 20
	if more {
		items = items[:20]
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"items": items, "hasMore": more}})
}
func (a *bugReportAPI) lookup(c *gin.Context) (bugLink, bool) {
	owner, ok := bugOwner(c)
	if !ok || !a.available(c) {
		return bugLink{}, false
	}
	id, err := uuid.Parse(c.Param("reportID"))
	if err != nil {
		driverError(c, 404, "report_not_found", "Bug report not found")
		return bugLink{}, false
	}
	var l bugLink
	err = a.pool.QueryRow(c.Request.Context(), `SELECT id::TEXT,created_at,COALESCE(external_id,'') FROM bug_report_links WHERE id=$1 AND provider=$2 AND ($3='' OR owner_id::TEXT=$3)`, id, a.provider.Name(), owner).Scan(&l.ID, &l.CreatedAt, &l.ExternalID)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 404, "report_not_found", "Bug report not found")
		return l, false
	}
	if err != nil {
		a.unavailable(c)
		return l, false
	}
	if l.ExternalID == "" {
		driverError(c, 409, "report_delivery_unconfirmed", "We could not confirm delivery. Contact support with reference "+l.ID+" before submitting again.")
		return l, false
	}
	return l, true
}
func (a *bugReportAPI) detail(c *gin.Context) {
	l, ok := a.lookup(c)
	if !ok {
		return
	}
	r, err := a.provider.Get(c.Request.Context(), l.ExternalID)
	if err != nil {
		a.unavailable(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"id": l.ID, "createdAt": l.CreatedAt, "title": r.Title, "description": r.Description, "status": r.Status, "comments": r.Comments}})
}
func (a *bugReportAPI) create(c *gin.Context) {
	owner, ok := bugOwner(c)
	if !ok || !a.available(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	var in struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		RequestID   string `json:"requestId"`
	}
	if c.ShouldBindJSON(&in) != nil {
		driverError(c, 400, "invalid_report", "Enter a title and description")
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	in.Description = strings.TrimSpace(in.Description)
	requestID, err := uuid.Parse(in.RequestID)
	if err != nil || utf8.RuneCountInString(in.Title) < 3 || utf8.RuneCountInString(in.Title) > 150 || utf8.RuneCountInString(in.Description) < 10 || utf8.RuneCountInString(in.Description) > 10000 {
		driverError(c, 400, "invalid_report", "Use a title of 3–150 characters, a description of 10–10000 characters and a valid requestId")
		return
	}
	raw, _ := json.Marshal([]string{in.Title, in.Description})
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	id := uuid.NewString()
	result, err := a.pool.Exec(c.Request.Context(), `INSERT INTO bug_report_links(id,owner_id,provider,request_id,request_hash) VALUES($1,$2,$3,$4,$5) ON CONFLICT(owner_id,request_id) DO NOTHING`, id, owner, a.provider.Name(), requestID, hash)
	if err != nil {
		a.unavailable(c)
		return
	}
	if result.RowsAffected() == 0 {
		var external, oldHash, provider string
		err = a.pool.QueryRow(c.Request.Context(), `SELECT id::TEXT,COALESCE(external_id,''),request_hash,provider FROM bug_report_links WHERE owner_id=$1 AND request_id=$2`, owner, requestID).Scan(&id, &external, &oldHash, &provider)
		if err != nil {
			a.unavailable(c)
			return
		}
		if oldHash != hash || provider != a.provider.Name() {
			driverError(c, 409, "request_conflict", "This submission reference was already used for a different report")
			return
		}
		if external != "" {
			c.JSON(200, contracts.APIResponse{Data: gin.H{"id": id, "delivery": "saved"}})
			return
		}
		driverError(c, 409, "report_delivery_unconfirmed", "We could not confirm delivery. Contact support with reference "+id+" before submitting again.")
		return
	}
	report := bugreports.Input{Title: in.Title, Description: in.Description, Reference: id}
	if user, ok := gatewayauth.CurrentUser(c); ok && user.HasRole("driver") {
		report.Title = "[Driver] " + report.Title
		report.Description = "Source: HeyGo Driver\n\n" + report.Description
	}
	external, err := a.provider.Create(c.Request.Context(), report)
	if err == nil {
		// Persist the successful provider reference even if the client disconnected.
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 3*time.Second)
		defer cancel()
		_, err = a.pool.Exec(saveCtx, `UPDATE bug_report_links SET external_id=$2 WHERE id=$1`, id, external)
	}
	if err != nil {
		driverError(c, 502, "report_delivery_unconfirmed", "We could not confirm delivery. Contact support with reference "+id+" before submitting again.")
		return
	}
	c.JSON(201, contracts.APIResponse{Data: gin.H{"id": id, "delivery": "saved"}})
}
func (a *bugReportAPI) comment(c *gin.Context) {
	l, ok := a.lookup(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
	var in struct {
		Body string `json:"body"`
	}
	if c.ShouldBindJSON(&in) != nil || len(strings.TrimSpace(in.Body)) == 0 || len(in.Body) > 4000 {
		driverError(c, 400, "invalid_comment", "Enter a comment of up to 4000 characters")
		return
	}
	if a.auditIntent(c, l.ID, "comment", "") != nil {
		a.unavailable(c)
		return
	}
	if a.provider.Comment(c.Request.Context(), l.ExternalID, in.Body) != nil {
		driverError(c, 502, "comment_delivery_unconfirmed", "Comment delivery could not be confirmed. Refresh the report before trying again.")
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"saved": true}})
}
func (a *bugReportAPI) setStatus(c *gin.Context) {
	l, ok := a.lookup(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<10)
	var in struct {
		Status string `json:"status"`
	}
	if c.ShouldBindJSON(&in) != nil {
		driverError(c, 400, "invalid_status", "Select a status")
		return
	}
	switch in.Status {
	case "open", "in_progress", "waiting_on_user", "resolved", "closed":
	default:
		driverError(c, 400, "invalid_status", "Select a valid status")
		return
	}
	if a.auditIntent(c, l.ID, "status", in.Status) != nil {
		a.unavailable(c)
		return
	}
	if a.provider.SetStatus(c.Request.Context(), l.ExternalID, in.Status) != nil {
		a.unavailable(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"saved": true}})
}
func (a *bugReportAPI) unavailable(c *gin.Context) {
	driverError(c, 503, "bug_reporting_unavailable", "Bug reporting is currently unavailable. Please try again later.")
}

// Record staff intent before a remote write. This deliberately describes an
// attempted action: a provider timeout does not establish whether it was saved.
func (a *bugReportAPI) auditIntent(c *gin.Context, id, action, status string) error {
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(c.Request.Context())
	details := gin.H{"status": status}
	if err = insertAdminAudit(c, tx, currentAdmin(c).ID, "bug_report."+action+".requested", "", "bug_report", id, details, nil, details); err != nil {
		return err
	}
	return tx.Commit(c.Request.Context())
}
