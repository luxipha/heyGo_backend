package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

type driverVerificationNote struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"createdAt"`
}

func registerAdminVerificationNoteRoutes(group *gin.RouterGroup, a *adminAPI) {
	group.GET("/drivers/:driverID/verification-notes", a.listVerificationNotes)
	group.POST("/drivers/:driverID/verification-notes", a.requireCSRF, a.createVerificationNote)
}

func (a *adminAPI) listVerificationNotes(ctx *gin.Context) {
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	rows, err := a.pool.Query(ctx.Request.Context(), `SELECT n.id::TEXT,n.body,COALESCE(u.username,'Staff'),n.created_at
		FROM driver_verification_notes n JOIN admin_users u ON u.id=n.admin_id
		WHERE n.driver_id=$1::UUID ORDER BY n.created_at DESC,n.id DESC LIMIT 100`, driver)
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer rows.Close()
	notes := make([]driverVerificationNote, 0)
	for rows.Next() {
		var note driverVerificationNote
		if err := rows.Scan(&note.ID, &note.Body, &note.Author, &note.CreatedAt); err != nil {
			reviewFailure(ctx)
			return
		}
		notes = append(notes, note)
	}
	if err := rows.Err(); err != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusOK, contracts.APIResponse{Data: notes})
}

func (a *adminAPI) createVerificationNote(ctx *gin.Context) {
	driver, ok := reviewDriverID(ctx)
	if !ok {
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if ctx.ShouldBindJSON(&body) != nil || len([]rune(strings.TrimSpace(body.Body))) > 2000 || strings.TrimSpace(body.Body) == "" {
		driverError(ctx, http.StatusBadRequest, "invalid_verification_note", "A note between 1 and 2000 characters is required")
		return
	}
	tx, err := a.pool.Begin(ctx.Request.Context())
	if err != nil {
		reviewFailure(ctx)
		return
	}
	defer tx.Rollback(ctx.Request.Context())
	var exists bool
	if err = tx.QueryRow(ctx.Request.Context(), `SELECT EXISTS(SELECT 1 FROM driver_profiles WHERE driver_id=$1::UUID)`, driver).Scan(&exists); err != nil || !exists {
		driverError(ctx, http.StatusNotFound, "driver_not_found", "Driver was not found")
		return
	}
	noteID := uuid.New()
	noteBody := strings.TrimSpace(body.Body)
	_, err = tx.Exec(ctx.Request.Context(), `INSERT INTO driver_verification_notes(id,driver_id,admin_id,body) VALUES($1,$2::UUID,$3::UUID,$4)`, noteID, driver, currentAdmin(ctx).ID, noteBody)
	if err == nil {
		err = adminAudit(ctx, tx, "driver.verification_note.created", driver, gin.H{"noteId": noteID.String()})
	}
	if err != nil || tx.Commit(ctx.Request.Context()) != nil {
		reviewFailure(ctx)
		return
	}
	ctx.JSON(http.StatusCreated, contracts.APIResponse{Data: gin.H{"id": noteID.String(), "body": noteBody}})
}
