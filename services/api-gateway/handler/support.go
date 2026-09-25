package handler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"github.com/luxipha/heyGo_backend/shared/messaging"
	"github.com/luxipha/heyGo_backend/shared/storage"
)

const (
	supportUploadLimit    int64 = 10 << 20
	supportUploadTTL            = 15 * time.Minute
	supportPageDefault          = 25
	supportPageMaximum          = 100
	supportMessageMaximum       = 4000
)

type supportAPI struct {
	pool  *pgxpool.Pool
	files storage.ObjectStore
}

type supportCursor struct {
	at time.Time
	id string
}

type supportMessage struct {
	ID            string              `json:"id"`
	CaseID        string              `json:"caseId"`
	SenderKind    string              `json:"senderKind"`
	SenderID      string              `json:"senderId,omitempty"`
	SenderName    string              `json:"senderName"`
	ClientMessage string              `json:"clientMessageId"`
	Body          string              `json:"body"`
	CreatedAt     time.Time           `json:"createdAt"`
	Attachments   []supportAttachment `json:"attachments,omitempty"`
}

type supportAttachment struct {
	ID          string `json:"id"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
	DownloadURL string `json:"downloadUrl,omitempty"`
}

func registerSupportRoutes(authenticated *gin.RouterGroup, pool *pgxpool.Pool, files storage.ObjectStore) {
	a := &supportAPI{pool: pool, files: files}
	authenticated.GET("/support/topics", a.userTopics)
	authenticated.POST("/support/uploads", a.createUpload)
	authenticated.POST("/trips/:tripID/issues", a.createTripIssue)
	authenticated.POST("/support/tickets", a.createTicket)
	authenticated.GET("/support/cases", a.userCases)
	authenticated.GET("/support/cases/:caseID", a.userCase)
	authenticated.GET("/support/cases/:caseID/messages", a.userMessages)
	authenticated.POST("/support/cases/:caseID/messages", a.userMessage)
	authenticated.POST("/support/cases/:caseID/read", a.userRead)
}

func registerAdminSupportRoutes(admin *gin.RouterGroup, pool *pgxpool.Pool, files storage.ObjectStore, csrf gin.HandlerFunc) {
	a := &supportAPI{pool: pool, files: files}
	admin.GET("/support/topics", a.adminTopics)
	admin.POST("/support/topics", csrf, a.createTopic)
	admin.PATCH("/support/topics/:code", csrf, a.updateTopic)
	admin.GET("/support/cases", a.adminCases)
	admin.GET("/support/cases/:caseID", a.adminCase)
	admin.GET("/support/cases/:caseID/messages", a.adminMessages)
	admin.POST("/support/cases/:caseID/messages", csrf, a.adminMessage)
	admin.PATCH("/support/cases/:caseID", csrf, a.updateCase)
	admin.POST("/support/cases/:caseID/read", csrf, a.adminRead)
}

func supportActor(ctx *gin.Context) (id, kind string, ok bool) {
	user, authenticated := gatewayauth.CurrentUser(ctx)
	if !authenticated {
		driverError(ctx, http.StatusUnauthorized, "authentication_required", "Authentication is required")
		return "", "", false
	}
	if user.HasRole("driver") {
		return user.ID, "driver", true
	}
	if user.HasRole("rider") {
		return user.ID, "rider", true
	}
	driverError(ctx, http.StatusForbidden, "support_role_required", "A rider or driver account is required")
	return "", "", false
}

func validSupportContentType(value string) bool {
	switch value {
	case "image/jpeg", "image/png", "application/pdf":
		return true
	default:
		return false
	}
}

func (a *supportAPI) userTopics(ctx *gin.Context) {
	_, audience, ok := supportActor(ctx)
	if !ok {
		return
	}
	kind := ctx.DefaultQuery("kind", "support")
	if kind != "support" && kind != "trip_issue" {
		driverError(ctx, http.StatusBadRequest, "invalid_topic_kind", "Topic kind must be support or trip_issue")
		return
	}
	items, err := a.readTopics(ctx, kind, audience)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Support topics are unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"items": items}})
}

func (a *supportAPI) readTopics(ctx *gin.Context, kind, audience string) ([]gin.H, error) {
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT code,label,details,sort_order FROM support_topics
		WHERE topic_kind=$1 AND enabled AND (audience='both' OR audience=$2) ORDER BY sort_order,code`, kind, audience)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var code, label string
		var details json.RawMessage
		var order int
		if err := rows.Scan(&code, &label, &details, &order); err != nil {
			return nil, err
		}
		items = append(items, gin.H{"code": code, "label": label, "details": details, "sortOrder": order})
	}
	return items, rows.Err()
}

func (a *supportAPI) createUpload(ctx *gin.Context) {
	if a.files == nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_storage_unavailable", "Evidence storage is not configured")
		return
	}
	owner, _, ok := supportActor(ctx)
	if !ok {
		return
	}
	var body struct {
		ContentType string `json:"contentType"`
		SizeBytes   int64  `json:"sizeBytes"`
	}
	if ctx.ShouldBindJSON(&body) != nil || !validSupportContentType(body.ContentType) || body.SizeBytes <= 0 || body.SizeBytes > supportUploadLimit {
		driverError(ctx, http.StatusBadRequest, "invalid_support_upload", "Evidence must be a JPEG, PNG, or PDF no larger than 10 MB")
		return
	}
	id := uuid.New()
	key := fmt.Sprintf("support/%s/%s", owner, id.String())
	expiresAt := time.Now().UTC().Add(supportUploadTTL)
	if _, err := a.pool.Exec(ctx.Request.Context(), `INSERT INTO support_uploads(id,owner_id,storage_key,content_type,size_bytes,expires_at)
		VALUES($1::UUID,$2::UUID,$3,$4,$5,$6)`, id, owner, key, body.ContentType, body.SizeBytes, expiresAt); err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_upload_unavailable", "Evidence upload could not be reserved")
		return
	}
	url, headers, err := a.files.PresignPut(ctx.Request.Context(), key, body.ContentType, body.SizeBytes, supportUploadTTL)
	if err != nil {
		_, _ = a.pool.Exec(ctx.Request.Context(), `DELETE FROM support_uploads WHERE id=$1::UUID`, id)
		driverError(ctx, http.StatusServiceUnavailable, "support_upload_unavailable", "Evidence upload URL could not be created")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"uploadId": id.String(), "url": url, "method": "PUT", "headers": headers, "expiresAt": expiresAt}})
}

type createSupportCaseBody struct {
	TopicCode   string   `json:"topicCode" binding:"required"`
	DetailCode  string   `json:"detailCode"`
	Subject     string   `json:"subject"`
	Description string   `json:"description"`
	UploadIDs   []string `json:"uploadIds"`
	ClientID    string   `json:"clientMessageId"`
}

func (a *supportAPI) createTripIssue(ctx *gin.Context) {
	owner, audience, ok := supportActor(ctx)
	if !ok {
		return
	}
	tripID, err := uuid.Parse(ctx.Param("tripID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_trip_id", "Valid trip ID is required")
		return
	}
	var body createSupportCaseBody
	if ctx.ShouldBindJSON(&body) != nil || strings.TrimSpace(body.TopicCode) == "" {
		driverError(ctx, http.StatusBadRequest, "invalid_issue", "A trip issue topic and description are required")
		return
	}
	if body.ClientID == "" {
		body.ClientID = uuid.NewString()
	}
	if err := validateSupportMessage(body.Description, body.UploadIDs, body.ClientID); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_issue", err.Error())
		return
	}
	if len([]rune(strings.TrimSpace(body.Description))) > 500 {
		driverError(ctx, http.StatusBadRequest, "invalid_issue", "Trip issue description must be 500 characters or fewer")
		return
	}
	var tripRider, tripDriver string
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT rider_id::TEXT,COALESCE(assigned_driver_id::TEXT,'') FROM trips WHERE id=$1::UUID`, tripID.String()).Scan(&tripRider, &tripDriver)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "trip_not_found", "Trip was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Trip issue could not be submitted")
		return
	}
	if tripRider == owner {
		audience = "rider"
	} else if tripDriver == owner {
		audience = "driver"
	} else {
		driverError(ctx, http.StatusForbidden, "trip_issue_forbidden", "Only a participant in this trip can report an issue")
		return
	}
	priority := "normal"
	if body.TopicCode == "safety" {
		priority = "high"
	}
	caseID, err := a.createCase(ctx, owner, audience, body, tripID.String(), "trip_issue", priority)
	if err != nil {
		supportFailure(ctx, err)
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"caseId": caseID, "status": "open"}})
}

func (a *supportAPI) createTicket(ctx *gin.Context) {
	owner, audience, ok := supportActor(ctx)
	if !ok {
		return
	}
	var body createSupportCaseBody
	if ctx.ShouldBindJSON(&body) != nil || strings.TrimSpace(body.TopicCode) == "" {
		driverError(ctx, http.StatusBadRequest, "invalid_ticket", "A support topic and initial message are required")
		return
	}
	if body.ClientID == "" {
		body.ClientID = uuid.NewString()
	}
	if err := validateSupportMessage(body.Description, body.UploadIDs, body.ClientID); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_ticket", err.Error())
		return
	}
	if strings.TrimSpace(body.Subject) == "" {
		body.Subject = "Support request"
	}
	caseID, err := a.createCase(ctx, owner, audience, body, "", "support", "normal")
	if err != nil {
		supportFailure(ctx, err)
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"caseId": caseID, "status": "open"}})
}

func validateSupportMessage(body string, uploadIDs []string, clientID string) error {
	if strings.TrimSpace(body) == "" || len([]rune(strings.TrimSpace(body))) > supportMessageMaximum {
		return fmt.Errorf("message must contain 1 to %d characters", supportMessageMaximum)
	}
	if len(uploadIDs) > 3 {
		return errors.New("at most 3 evidence files are allowed")
	}
	if _, err := uuid.Parse(clientID); err != nil {
		return errors.New("clientMessageId must be a UUID")
	}
	for _, id := range uploadIDs {
		if _, err := uuid.Parse(id); err != nil {
			return errors.New("uploadIds must contain UUIDs")
		}
	}
	return nil
}

func (a *supportAPI) createCase(ctx *gin.Context, owner, audience string, body createSupportCaseBody, tripID, topicKind, priority string) (string, error) {
	var topicLabel string
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT label FROM support_topics WHERE code=$1 AND topic_kind=$2 AND enabled AND (audience='both' OR audience=$3)`, body.TopicCode, topicKind, audience).Scan(&topicLabel)
	if err != nil {
		return "", errors.New("support topic is not available")
	}
	if body.Subject == "" {
		body.Subject = topicLabel
	}
	if len([]rune(strings.TrimSpace(body.Subject))) > 160 {
		return "", errors.New("subject must be at most 160 characters")
	}
	if len(body.UploadIDs) > 0 && a.files == nil {
		return "", errors.New("evidence storage is not configured")
	}
	if err := a.verifySupportUploads(ctx, owner, body.UploadIDs); err != nil {
		return "", err
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx.Request.Context())
	caseID, messageID := uuid.NewString(), uuid.NewString()
	var trip any
	if tripID != "" {
		trip = tripID
	}
	if _, err = tx.Exec(ctx.Request.Context(), `INSERT INTO support_cases(id,opened_by,audience,topic_code,trip_id,detail_code,subject,priority)
		VALUES($1::UUID,$2::UUID,$3,$4,$5::UUID,$6,$7,$8)`, caseID, owner, audience, body.TopicCode, trip, strings.TrimSpace(body.DetailCode), strings.TrimSpace(body.Subject), priority); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx.Request.Context(), `INSERT INTO support_case_messages(id,case_id,sender_kind,sender_user_id,client_message_id,body)
		VALUES($1::UUID,$2::UUID,'user',$3::UUID,$4::UUID,$5)`, messageID, caseID, owner, body.ClientID, strings.TrimSpace(body.Description)); err != nil {
		return "", err
	}
	if err = consumeSupportUploads(ctx, tx, owner, messageID, body.UploadIDs); err != nil {
		return "", err
	}
	if _, err = messaging.AppendEventTx(ctx.Request.Context(), tx, owner, "support-case-created:"+caseID, "support.case.updated", json.RawMessage(`{"caseId":"`+caseID+`","status":"open"}`)); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx.Request.Context()); err != nil {
		return "", err
	}
	return caseID, nil
}

func (a *supportAPI) verifySupportUploads(ctx *gin.Context, owner string, ids []string) error {
	for _, id := range ids {
		var key, contentType string
		var size int64
		err := a.pool.QueryRow(ctx.Request.Context(), `SELECT storage_key,content_type,size_bytes FROM support_uploads WHERE id=$1::UUID AND owner_id=$2::UUID AND consumed_at IS NULL AND expires_at>NOW()`, id, owner).Scan(&key, &contentType, &size)
		if err != nil {
			return errors.New("evidence upload is invalid, expired, or already used")
		}
		info, err := a.files.Head(ctx.Request.Context(), key)
		if err != nil || info.Size != size || !strings.EqualFold(info.ContentType, contentType) {
			return errors.New("evidence upload is incomplete or does not match its reservation")
		}
	}
	return nil
}

func consumeSupportUploads(ctx *gin.Context, tx pgx.Tx, owner, messageID string, ids []string) error {
	for _, id := range ids {
		var key, contentType string
		var size int64
		err := tx.QueryRow(ctx.Request.Context(), `UPDATE support_uploads SET consumed_at=NOW()
			WHERE id=$1::UUID AND owner_id=$2::UUID AND consumed_at IS NULL AND expires_at>NOW()
			RETURNING storage_key,content_type,size_bytes`, id, owner).Scan(&key, &contentType, &size)
		if err != nil {
			return errors.New("evidence upload is invalid, expired, or already used")
		}
		if _, err = tx.Exec(ctx.Request.Context(), `INSERT INTO support_case_attachments(id,message_id,storage_key,content_type,size_bytes)
			VALUES($1::UUID,$2::UUID,$3,$4,$5)`, uuid.NewString(), messageID, key, contentType, size); err != nil {
			return err
		}
	}
	return nil
}

func supportFailure(ctx *gin.Context, err error) {
	if strings.Contains(err.Error(), "support topic is not available") || strings.Contains(err.Error(), "evidence upload") || strings.Contains(err.Error(), "message must contain") || strings.Contains(err.Error(), "client message ID was reused") || strings.Contains(err.Error(), "subject must be") {
		driverError(ctx, http.StatusBadRequest, "invalid_support_case", err.Error())
		return
	}
	if strings.Contains(err.Error(), "support case is closed") {
		driverError(ctx, http.StatusConflict, "support_case_closed", "This support case is closed")
		return
	}
	if strings.Contains(err.Error(), "support case not found") {
		driverError(ctx, http.StatusNotFound, "support_case_not_found", "Support case was not found")
		return
	}
	driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Support case could not be saved")
}

func parseSupportLimit(raw string) (int, error) {
	if raw == "" {
		return supportPageDefault, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > supportPageMaximum {
		return 0, errors.New("limit must be between 1 and 100")
	}
	return limit, nil
}

func parseSupportCursor(raw string) (*supportCursor, error) {
	if raw == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("invalid cursor")
	}
	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 {
		return nil, errors.New("invalid cursor")
	}
	at, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return nil, errors.New("invalid cursor")
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return nil, errors.New("invalid cursor")
	}
	return &supportCursor{at: at.UTC(), id: id.String()}, nil
}

func supportNextCursor(at time.Time, id string) *string {
	value := base64.RawURLEncoding.EncodeToString([]byte(at.UTC().Format(time.RFC3339Nano) + "|" + id))
	return &value
}

func (a *supportAPI) userCases(ctx *gin.Context) {
	owner, _, ok := supportActor(ctx)
	if !ok {
		return
	}
	a.listCases(ctx, owner, false)
}

func (a *supportAPI) adminCases(ctx *gin.Context) { a.listCases(ctx, "", true) }

func (a *supportAPI) listCases(ctx *gin.Context, owner string, staff bool) {
	limit, err := parseSupportLimit(ctx.Query("limit"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}
	cursor, err := parseSupportCursor(ctx.Query("cursor"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_cursor", err.Error())
		return
	}
	status := ctx.DefaultQuery("status", "all")
	if !validSupportFilter(status) {
		driverError(ctx, http.StatusBadRequest, "invalid_status", "Support status filter is invalid")
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT c.id::TEXT,c.case_number,c.topic_code,t.label,c.trip_id::TEXT,c.subject,c.status,c.priority,
		c.created_at,c.updated_at,c.last_message_at,c.assigned_admin_id::TEXT,
		c.opened_by::TEXT,u.human_id,
		COALESCE((SELECT COUNT(*) FROM support_case_messages m LEFT JOIN support_case_reads r ON r.case_id=c.id AND r.reader_kind=$2 AND r.reader_id=$3::UUID
			WHERE m.case_id=c.id AND m.sender_kind<>$2 AND m.created_at>COALESCE(r.read_at,'epoch'::TIMESTAMPTZ)),0)::INT
		FROM support_cases c JOIN support_topics t ON t.code=c.topic_code JOIN users u ON u.id=c.opened_by
		WHERE ($1::UUID IS NULL OR c.opened_by=$1::UUID) AND ($4='all' OR c.status=$4)
		AND ($5::TIMESTAMPTZ IS NULL OR (c.last_message_at,c.id)<($5,$6::UUID))
		ORDER BY c.last_message_at DESC,c.id DESC LIMIT $7`, nullableSupportOwner(owner), supportReaderKind(staff), supportReaderID(ctx, staff, owner), status, supportCursorTime(cursor), supportCursorID(cursor), limit+1)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Support cases are unavailable")
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0, limit+1)
	var lastAt time.Time
	var lastID string
	for rows.Next() {
		var id, topic, label, subject, state, priority, assigned, requester, humanID string
		var trip *string
		var number int64
		var created, updated, last time.Time
		var unread int
		if err := rows.Scan(&id, &number, &topic, &label, &trip, &subject, &state, &priority, &created, &updated, &last, &assigned, &requester, &humanID, &unread); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Support cases are unavailable")
			return
		}
		item := gin.H{"id": id, "reference": fmt.Sprintf("HG-%08d", number), "topicCode": topic, "topicLabel": label, "tripId": trip, "subject": subject, "status": state, "priority": priority, "assignedAdminId": emptyAsNil(assigned), "createdAt": created, "updatedAt": updated, "lastMessageAt": last, "unreadCount": unread}
		if staff {
			item["requesterId"] = requester
			item["requesterHumanId"] = humanID
		}
		items = append(items, item)
		lastAt, lastID = last, id
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Support cases are unavailable")
		return
	}
	var next *string
	if len(items) > limit {
		trimmed := items[limit-1]
		lastAt, _ = trimmed["lastMessageAt"].(time.Time)
		lastID, _ = trimmed["id"].(string)
		next = supportNextCursor(lastAt, lastID)
		items = items[:limit]
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"items": items}, Meta: contracts.APIMeta{NextCursor: next}})
}

func nullableSupportOwner(owner string) any {
	if owner == "" {
		return nil
	}
	return owner
}
func supportReaderKind(staff bool) string {
	if staff {
		return "staff"
	}
	return "user"
}
func supportReaderID(ctx *gin.Context, staff bool, owner string) string {
	if staff {
		return currentAdmin(ctx).ID
	}
	return owner
}
func supportCursorTime(cursor *supportCursor) any {
	if cursor == nil {
		return nil
	}
	return cursor.at
}
func supportCursorID(cursor *supportCursor) any {
	if cursor == nil {
		return nil
	}
	return cursor.id
}
func emptyAsNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func validSupportFilter(value string) bool {
	switch value {
	case "all", "open", "in_progress", "waiting_on_user", "resolved", "closed":
		return true
	}
	return false
}

func supportCaseID(ctx *gin.Context) (string, bool) {
	id, err := uuid.Parse(ctx.Param("caseID"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_case_id", "Valid support case ID is required")
		return "", false
	}
	return id.String(), true
}

func (a *supportAPI) userCase(ctx *gin.Context) {
	owner, _, ok := supportActor(ctx)
	if !ok {
		return
	}
	caseID, ok := supportCaseID(ctx)
	if !ok {
		return
	}
	a.readCase(ctx, caseID, owner, false)
}

func (a *supportAPI) adminCase(ctx *gin.Context) {
	caseID, ok := supportCaseID(ctx)
	if !ok {
		return
	}
	a.readCase(ctx, caseID, "", true)
}

func (a *supportAPI) readCase(ctx *gin.Context, caseID, owner string, staff bool) {
	var item gin.H
	var opener, requesterHumanID, topic, label, subject, state, priority, audience string
	var trip *string
	var number int64
	var created, updated time.Time
	var assigned string
	query := `SELECT c.opened_by::TEXT,u.human_id,c.audience,c.case_number,c.topic_code,t.label,c.trip_id::TEXT,c.subject,c.status,c.priority,c.created_at,c.updated_at,COALESCE(c.assigned_admin_id::TEXT,'')
		FROM support_cases c JOIN support_topics t ON t.code=c.topic_code JOIN users u ON u.id=c.opened_by WHERE c.id=$1::UUID AND ($2::UUID IS NULL OR c.opened_by=$2::UUID)`
	err := a.pool.QueryRow(ctx.Request.Context(), query, caseID, nullableSupportOwner(owner)).Scan(&opener, &requesterHumanID, &audience, &number, &topic, &label, &trip, &subject, &state, &priority, &created, &updated, &assigned)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "support_case_not_found", "Support case was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Support case is unavailable")
		return
	}
	item = gin.H{"id": caseID, "reference": fmt.Sprintf("HG-%08d", number), "openedBy": opener, "audience": audience, "topicCode": topic, "topicLabel": label, "tripId": trip, "subject": subject, "status": state, "priority": priority, "assignedAdminId": emptyAsNil(assigned), "createdAt": created, "updatedAt": updated}
	if staff {
		item["requesterHumanId"] = requesterHumanID
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: item})
}

func (a *supportAPI) messageAttachments(ctx *gin.Context, messageID string) ([]supportAttachment, error) {
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT id::TEXT,storage_key,content_type,size_bytes FROM support_case_attachments WHERE message_id=$1::UUID ORDER BY id`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]supportAttachment, 0)
	for rows.Next() {
		var item supportAttachment
		var key string
		if err := rows.Scan(&item.ID, &key, &item.ContentType, &item.SizeBytes); err != nil {
			return nil, err
		}
		if a.files != nil {
			item.DownloadURL, err = a.files.PresignGet(ctx.Request.Context(), key, 5*time.Minute)
			if err != nil {
				return nil, err
			}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (a *supportAPI) userMessages(ctx *gin.Context) {
	owner, _, ok := supportActor(ctx)
	if !ok {
		return
	}
	caseID, ok := supportCaseID(ctx)
	if !ok {
		return
	}
	a.listMessages(ctx, caseID, owner, false)
}

func (a *supportAPI) adminMessages(ctx *gin.Context) {
	caseID, ok := supportCaseID(ctx)
	if !ok {
		return
	}
	a.listMessages(ctx, caseID, "", true)
}

func (a *supportAPI) listMessages(ctx *gin.Context, caseID, owner string, staff bool) {
	var exists bool
	err := a.pool.QueryRow(ctx.Request.Context(), `SELECT TRUE FROM support_cases WHERE id=$1::UUID AND ($2::UUID IS NULL OR opened_by=$2::UUID)`, caseID, nullableSupportOwner(owner)).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "support_case_not_found", "Support case was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Support conversation is unavailable")
		return
	}
	limit, err := parseSupportLimit(ctx.Query("limit"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}
	cursor, err := parseSupportCursor(ctx.Query("cursor"))
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_cursor", err.Error())
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT m.id::TEXT,m.case_id::TEXT,m.sender_kind,COALESCE(m.sender_user_id::TEXT,m.sender_admin_id::TEXT,''),m.client_message_id::TEXT,m.body,m.created_at,c.audience
		FROM support_case_messages m JOIN support_cases c ON c.id=m.case_id WHERE m.case_id=$1::UUID AND ($2::TIMESTAMPTZ IS NULL OR (m.created_at,m.id)<($2,$3::UUID)) ORDER BY m.created_at DESC,m.id DESC LIMIT $4`, caseID, supportCursorTime(cursor), supportCursorID(cursor), limit+1)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Support conversation is unavailable")
		return
	}
	defer rows.Close()
	items := make([]supportMessage, 0, limit+1)
	for rows.Next() {
		var message supportMessage
		var audience string
		if err := rows.Scan(&message.ID, &message.CaseID, &message.SenderKind, &message.SenderID, &message.ClientMessage, &message.Body, &message.CreatedAt, &audience); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Support conversation is unavailable")
			return
		}
		if message.SenderKind == "staff" {
			message.SenderName = "HeyGo Support"
		} else if staff {
			message.SenderName = strings.ToUpper(audience[:1]) + audience[1:]
		} else {
			message.SenderName = "You"
		}
		message.Attachments, err = a.messageAttachments(ctx, message.ID)
		if err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Support attachments are unavailable")
			return
		}
		items = append(items, message)
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Support conversation is unavailable")
		return
	}
	var next *string
	if len(items) > limit {
		last := items[limit-1]
		next = supportNextCursor(last.CreatedAt, last.ID)
		items = items[:limit]
	}
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"items": items}, Meta: contracts.APIMeta{NextCursor: next}})
}

func (a *supportAPI) userMessage(ctx *gin.Context) {
	owner, _, ok := supportActor(ctx)
	if !ok {
		return
	}
	caseID, ok := supportCaseID(ctx)
	if !ok {
		return
	}
	var body supportMessageBody
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_message", "Message text and clientMessageId are required")
		return
	}
	if err := validateSupportMessage(body.Body, body.UploadIDs, body.ClientMessageID); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_message", err.Error())
		return
	}
	if err := a.appendMessage(ctx, caseID, owner, "user", body); err != nil {
		supportFailure(ctx, err)
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"accepted": true}})
}

type supportMessageBody struct {
	ClientMessageID string   `json:"clientMessageId" binding:"required"`
	Body            string   `json:"body" binding:"required"`
	UploadIDs       []string `json:"uploadIds"`
}

func (a *supportAPI) appendMessage(ctx *gin.Context, caseID, senderID, kind string, body supportMessageBody) error {
	if len(body.UploadIDs) > 0 && a.files == nil {
		return errors.New("evidence storage is not configured")
	}
	if kind == "user" && len(body.UploadIDs) > 0 {
		if err := a.verifySupportUploads(ctx, senderID, body.UploadIDs); err != nil {
			return err
		}
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx.Request.Context())
	var owner, caseStatus string
	err = tx.QueryRow(ctx.Request.Context(), `SELECT opened_by::TEXT,status FROM support_cases WHERE id=$1::UUID FOR UPDATE`, caseID).Scan(&owner, &caseStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("support case not found")
	}
	if err != nil {
		return err
	}
	if caseStatus == "resolved" || caseStatus == "closed" {
		return errors.New("support case is closed")
	}
	clientID, _ := uuid.Parse(body.ClientMessageID)
	messageID := uuid.NewString()
	var senderUser, senderAdmin any
	if kind == "user" {
		if senderID != owner {
			return errors.New("support case not found")
		}
		senderUser = senderID
	} else {
		senderAdmin = senderID
	}
	var returnedID string
	err = tx.QueryRow(ctx.Request.Context(), `INSERT INTO support_case_messages(id,case_id,sender_kind,sender_user_id,sender_admin_id,client_message_id,body)
		VALUES($1::UUID,$2::UUID,$3,$4::UUID,$5::UUID,$6::UUID,$7) ON CONFLICT DO NOTHING RETURNING id::TEXT`, messageID, caseID, kind, senderUser, senderAdmin, clientID.String(), strings.TrimSpace(body.Body)).Scan(&returnedID)
	if errors.Is(err, pgx.ErrNoRows) {
		var priorBody string
		err = tx.QueryRow(ctx.Request.Context(), `SELECT id::TEXT,body FROM support_case_messages WHERE case_id=$1::UUID AND sender_kind=$2
			AND COALESCE(sender_user_id::TEXT,sender_admin_id::TEXT)=$3 AND client_message_id=$4::UUID`, caseID, kind, senderID, clientID.String()).Scan(&returnedID, &priorBody)
		if err == nil && priorBody != strings.TrimSpace(body.Body) {
			return errors.New("client message ID was reused with different text")
		}
	}
	if err != nil {
		return err
	}
	created := returnedID == messageID
	if created {
		if err := consumeSupportUploads(ctx, tx, owner, returnedID, body.UploadIDs); err != nil {
			return err
		}
		if kind == "user" {
			if _, err = tx.Exec(ctx.Request.Context(), `UPDATE support_cases SET status='open',last_message_at=NOW(),updated_at=NOW() WHERE id=$1::UUID`, caseID); err != nil {
				return err
			}
		} else {
			if _, err = tx.Exec(ctx.Request.Context(), `UPDATE support_cases SET status='waiting_on_user',last_message_at=NOW(),updated_at=NOW() WHERE id=$1::UUID`, caseID); err != nil {
				return err
			}
		}
		if kind == "staff" {
			if err = adminAudit(ctx, tx, "support.case.replied", "", gin.H{"caseId": caseID, "openedBy": owner, "messageId": returnedID}); err != nil {
				return err
			}
		}
		payload, _ := json.Marshal(gin.H{"caseId": caseID, "messageId": returnedID, "senderKind": kind, "body": strings.TrimSpace(body.Body)})
		if kind == "staff" {
			if _, err = messaging.AppendEventTx(ctx.Request.Context(), tx, owner, "support-message:"+returnedID, "support.message.created", payload); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx.Request.Context())
}

func (a *supportAPI) adminMessage(ctx *gin.Context) {
	caseID, ok := supportCaseID(ctx)
	if !ok {
		return
	}
	admin := currentAdmin(ctx)
	var body supportMessageBody
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_message", "Message text and clientMessageId are required")
		return
	}
	if err := validateSupportMessage(body.Body, body.UploadIDs, body.ClientMessageID); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_message", err.Error())
		return
	}
	if err := a.appendMessage(ctx, caseID, admin.ID, "staff", body); err != nil {
		supportFailure(ctx, err)
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"accepted": true}})
}

func (a *supportAPI) userRead(ctx *gin.Context) {
	owner, _, ok := supportActor(ctx)
	if !ok {
		return
	}
	caseID, ok := supportCaseID(ctx)
	if !ok {
		return
	}
	a.markRead(ctx, caseID, owner, "user")
}
func (a *supportAPI) adminRead(ctx *gin.Context) {
	caseID, ok := supportCaseID(ctx)
	if !ok {
		return
	}
	a.markRead(ctx, caseID, currentAdmin(ctx).ID, "staff")
}

func (a *supportAPI) markRead(ctx *gin.Context, caseID, readerID, kind string) {
	var body struct {
		ThroughMessageID string `json:"throughMessageId" binding:"required"`
	}
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_read_receipt", "throughMessageId is required")
		return
	}
	through, err := uuid.Parse(body.ThroughMessageID)
	if err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_read_receipt", "throughMessageId must be a UUID")
		return
	}
	var owner string
	err = a.pool.QueryRow(ctx.Request.Context(), `SELECT opened_by::TEXT FROM support_cases WHERE id=$1::UUID`, caseID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) || (!((kind == "user" && readerID == owner) || kind == "staff") && err == nil) {
		driverError(ctx, http.StatusNotFound, "support_case_not_found", "Support case was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Support read state is unavailable")
		return
	}
	tag, err := a.pool.Exec(ctx.Request.Context(), `INSERT INTO support_case_reads(case_id,reader_kind,reader_id,read_at)
		SELECT $1::UUID,$2,$3::UUID,m.created_at FROM support_case_messages m WHERE m.case_id=$1::UUID AND m.id=$4::UUID
		ON CONFLICT(case_id,reader_kind,reader_id) DO UPDATE SET read_at=GREATEST(support_case_reads.read_at,EXCLUDED.read_at)`, caseID, kind, readerID, through.String())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Support read state could not be saved")
		return
	}
	if tag.RowsAffected() == 0 {
		driverError(ctx, http.StatusNotFound, "message_not_found", "Support message was not found")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"caseId": caseID, "throughMessageId": through.String()}})
}

func (a *supportAPI) adminTopics(ctx *gin.Context) {
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT code,topic_kind,audience,label,details,enabled,sort_order,updated_at FROM support_topics ORDER BY topic_kind,sort_order,code`)
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Support topics are unavailable")
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var code, kind, audience, label string
		var details json.RawMessage
		var enabled bool
		var sort int
		var updated time.Time
		if err := rows.Scan(&code, &kind, &audience, &label, &details, &enabled, &sort, &updated); err != nil {
			driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Support topics are unavailable")
			return
		}
		items = append(items, gin.H{"code": code, "kind": kind, "audience": audience, "label": label, "details": details, "enabled": enabled, "sortOrder": sort, "updatedAt": updated})
	}
	if rows.Err() != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Support topics are unavailable")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"items": items}})
}

type supportTopicInput struct {
	Code      string   `json:"code"`
	Kind      string   `json:"kind"`
	Audience  string   `json:"audience"`
	Label     string   `json:"label"`
	Details   []string `json:"details"`
	Enabled   *bool    `json:"enabled"`
	SortOrder *int     `json:"sortOrder"`
}

func validSupportTopicCode(code string) bool {
	if len(code) < 2 || len(code) > 48 {
		return false
	}
	for _, r := range code {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func validateSupportTopic(body supportTopicInput, creating bool) error {
	if creating && !validSupportTopicCode(body.Code) {
		return errors.New("topic code must use 2-48 lowercase letters, digits, hyphens, or underscores")
	}
	if body.Kind != "" && body.Kind != "support" && body.Kind != "trip_issue" {
		return errors.New("topic kind must be support or trip_issue")
	}
	if body.Audience != "" && body.Audience != "driver" && body.Audience != "rider" && body.Audience != "both" {
		return errors.New("audience must be driver, rider, or both")
	}
	if body.Label != "" && (len([]rune(strings.TrimSpace(body.Label))) > 120) {
		return errors.New("label must be at most 120 characters")
	}
	if creating && strings.TrimSpace(body.Label) == "" {
		return errors.New("label is required")
	}
	if len(body.Details) > 30 {
		return errors.New("at most 30 detail options are allowed")
	}
	for _, detail := range body.Details {
		if len([]rune(strings.TrimSpace(detail))) > 120 {
			return errors.New("detail options must be at most 120 characters")
		}
	}
	return nil
}

func (a *supportAPI) createTopic(ctx *gin.Context) {
	var body supportTopicInput
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_support_topic", "Topic fields are invalid")
		return
	}
	if err := validateSupportTopic(body, true); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_support_topic", err.Error())
		return
	}
	if body.Kind == "" || body.Audience == "" {
		driverError(ctx, http.StatusBadRequest, "invalid_support_topic", "Kind and audience are required")
		return
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	details, _ := json.Marshal(body.Details)
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Topic could not be saved")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	sortOrder := 0
	if body.SortOrder != nil {
		sortOrder = *body.SortOrder
	}
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO support_topics(code,topic_kind,audience,label,details,enabled,sort_order) VALUES($1,$2,$3,$4,$5::JSONB,$6,$7)`, body.Code, body.Kind, body.Audience, strings.TrimSpace(body.Label), details, enabled, sortOrder)
	if err == nil {
		err = adminAudit(ctx, tx, "support.topic.created", "", gin.H{"code": body.Code, "kind": body.Kind, "audience": body.Audience, "enabled": enabled})
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		driverError(ctx, http.StatusConflict, "support_topic_conflict", "Topic could not be created; its code may already exist")
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"code": body.Code, "enabled": enabled}})
}

func (a *supportAPI) updateTopic(ctx *gin.Context) {
	code := ctx.Param("code")
	if !validSupportTopicCode(code) {
		driverError(ctx, http.StatusBadRequest, "invalid_support_topic", "Valid topic code is required")
		return
	}
	var body supportTopicInput
	if ctx.ShouldBindJSON(&body) != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_support_topic", "Topic fields are invalid")
		return
	}
	if err := validateSupportTopic(body, false); err != nil {
		driverError(ctx, http.StatusBadRequest, "invalid_support_topic", err.Error())
		return
	}
	if body.Label == "" && body.Details == nil && body.Enabled == nil && body.SortOrder == nil {
		driverError(ctx, http.StatusBadRequest, "invalid_support_topic", "At least one topic field is required")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Topic could not be updated")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var oldLabel string
	var oldDetails json.RawMessage
	var oldEnabled bool
	var oldSortOrder int
	err = tx.QueryRow(ctx.Request.Context(), `SELECT label,details,enabled,sort_order FROM support_topics WHERE code=$1 FOR UPDATE`, code).Scan(&oldLabel, &oldDetails, &oldEnabled, &oldSortOrder)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "support_topic_not_found", "Support topic was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Topic could not be updated")
		return
	}
	label := body.Label
	if label == "" {
		label = oldLabel
	}
	details := oldDetails
	if body.Details != nil {
		details, err = json.Marshal(body.Details)
		if err != nil {
			driverError(ctx, http.StatusBadRequest, "invalid_support_topic", "Topic details are invalid")
			return
		}
	}
	enabled := oldEnabled
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	sortOrder := oldSortOrder
	if body.SortOrder != nil {
		sortOrder = *body.SortOrder
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE support_topics SET label=$2,details=$3::JSONB,enabled=$4,sort_order=$5,updated_at=NOW() WHERE code=$1`, code, strings.TrimSpace(label), details, enabled, sortOrder)
	if err == nil {
		err = adminAudit(ctx, tx, "support.topic.updated", "", gin.H{"code": code, "before": gin.H{"label": oldLabel, "details": oldDetails, "enabled": oldEnabled, "sortOrder": oldSortOrder}, "after": body})
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Topic could not be updated")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"code": code, "updated": true}})
}

func (a *supportAPI) updateCase(ctx *gin.Context) {
	caseID, ok := supportCaseID(ctx)
	if !ok {
		return
	}
	var body struct {
		Status          *string `json:"status"`
		AssignedAdminID *string `json:"assignedAdminId"`
	}
	if ctx.ShouldBindJSON(&body) != nil || (body.Status == nil && body.AssignedAdminID == nil) {
		driverError(ctx, http.StatusBadRequest, "invalid_case_update", "Status or assignedAdminId is required")
		return
	}
	if body.Status != nil && !validSupportFilter(*body.Status) {
		driverError(ctx, http.StatusBadRequest, "invalid_case_status", "Status is invalid")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Support case could not be updated")
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var owner, oldStatus string
	err = tx.QueryRow(ctx.Request.Context(), `SELECT opened_by::TEXT,status FROM support_cases WHERE id=$1::UUID FOR UPDATE`, caseID).Scan(&owner, &oldStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(ctx, http.StatusNotFound, "support_case_not_found", "Support case was not found")
		return
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Support case could not be updated")
		return
	}
	status := oldStatus
	if body.Status != nil {
		status = *body.Status
	}
	var assigned any
	if body.AssignedAdminID != nil {
		if *body.AssignedAdminID != "" {
			id, parseErr := uuid.Parse(*body.AssignedAdminID)
			if parseErr != nil {
				driverError(ctx, http.StatusBadRequest, "invalid_assignee", "assignedAdminId must be an active staff UUID or empty")
				return
			}
			var active bool
			if err = tx.QueryRow(ctx.Request.Context(), `SELECT active FROM admin_users WHERE id=$1::UUID`, id.String()).Scan(&active); err != nil || !active {
				driverError(ctx, http.StatusBadRequest, "invalid_assignee", "assignedAdminId must identify an active staff account")
				return
			}
			assigned = id.String()
		}
	} else {
		var prior string
		_ = tx.QueryRow(ctx.Request.Context(), `SELECT COALESCE(assigned_admin_id::TEXT,'') FROM support_cases WHERE id=$1::UUID`, caseID).Scan(&prior)
		if prior != "" {
			assigned = prior
		}
	}
	_, err = tx.Exec(ctx.Request.Context(), `UPDATE support_cases SET status=$2,assigned_admin_id=$3::UUID,updated_at=NOW() WHERE id=$1::UUID`, caseID, status, assigned)
	if err == nil {
		err = adminAudit(ctx, tx, "support.case.updated", "", gin.H{"caseId": caseID, "openedBy": owner, "statusBefore": oldStatus, "statusAfter": status, "assignedAdminId": assigned})
	}
	if err == nil {
		var payload []byte
		payload, err = json.Marshal(gin.H{"caseId": caseID, "status": status, "assignedAdminId": assigned})
		if err == nil {
			_, err = messaging.AppendEventTx(ctx.Request.Context(), tx, owner, "support-case-updated:"+caseID+":"+status+":"+uuid.NewString(), "support.case.updated", payload)
		}
	}
	if err == nil {
		err = tx.Commit(ctx.Request.Context())
	}
	if err != nil {
		driverError(ctx, http.StatusServiceUnavailable, "support_unavailable", "Support case could not be updated")
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"caseId": caseID, "status": status, "assignedAdminId": assigned}})
}
