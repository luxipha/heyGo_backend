package handler

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/contracts"
)

// vehicleDatabase keeps selected-car work on one transaction/connection. The
// transaction-local setting cannot leak into the next request on a pooled connection.
type vehicleDatabase interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func scopedVehicleDB(c *gin.Context, p *pgxpool.Pool) vehicleDatabase {
	if tx, ok := c.Get("vehicleTransaction"); ok {
		return tx.(pgx.Tx)
	}
	return p
}
func (a *driverAPI) vehicleDB(c *gin.Context) vehicleDatabase { return scopedVehicleDB(c, a.pool) }
func (a *adminAPI) vehicleDB(c *gin.Context) vehicleDatabase  { return scopedVehicleDB(c, a.pool) }

type vehicleResponse struct {
	gin.ResponseWriter
	body   bytes.Buffer
	status int
}

func (w *vehicleResponse) WriteHeader(s int) { w.status = s }
func (w *vehicleResponse) WriteHeaderNow()   {}
func (w *vehicleResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.body.Write(b)
}
func (w *vehicleResponse) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *vehicleResponse) Status() int {
	if w.status == 0 {
		return 200
	}
	return w.status
}
func (w *vehicleResponse) Size() int     { return w.body.Len() }
func (w *vehicleResponse) Written() bool { return w.status != 0 }

// Existing endpoints remain active-car APIs. An explicit vehicleId selects a
// different owned car for this request only; it never changes the active car.
func withVehicleScope(pool *pgxpool.Pool, next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		car := c.Query("vehicleId")
		c.Set("explicitVehicleScope", car != "")
		if _, e := uuid.Parse(car); car != "" && e != nil {
			driverError(c, 400, "invalid_vehicle_id", "Choose a valid vehicle")
			return
		}
		owner := c.Param("driverID")
		if owner == "" {
			owner = driverID(c)
		}
		tx, e := pool.Begin(c.Request.Context())
		if e != nil {
			reviewFailure(c)
			return
		}
		defer tx.Rollback(context.Background())
		// All fleet mutations use the user lock before profile/driver locks.
		if c.Request.Method != http.MethodGet && !c.GetBool("vehicleReadOnly") {
			if _, e = tx.Exec(c.Request.Context(), `SELECT id FROM users WHERE id=$1::uuid FOR UPDATE`, owner); e != nil {
				reviewFailure(c)
				return
			}
		}
		if car == "" {
			if e = tx.QueryRow(c.Request.Context(), `SELECT id::text FROM driver_vehicle_records WHERE driver_id=$1::uuid AND is_active`, owner).Scan(&car); e == pgx.ErrNoRows {
				_ = tx.Rollback(c.Request.Context())
				next(c)
				return
			} else if e != nil {
				reviewFailure(c)
				return
			}
		}
		var active bool
		if e = tx.QueryRow(c.Request.Context(), `SELECT is_active FROM driver_vehicle_records WHERE id=$1::uuid AND driver_id=$2::uuid`, car, owner).Scan(&active); e != nil {
			driverError(c, 404, "vehicle_not_found", "Vehicle was not found")
			return
		}
		if _, e = tx.Exec(c.Request.Context(), `SELECT set_config('heygo.vehicle_id',$1,TRUE)`, car); e != nil {
			reviewFailure(c)
			return
		}
		c.Set("vehicleTransaction", tx)
		c.Set("selectedVehicleId", car)
		c.Set("selectedVehicleActive", active)
		original := c.Writer
		buffer := &vehicleResponse{ResponseWriter: original}
		c.Writer = buffer
		defer func() { c.Writer = original; c.Set("vehicleTransaction", nil) }()
		next(c)
		c.Writer = original
		if buffer.Status() < 400 || c.GetBool("commitVehicleScope") {
			if e = tx.Commit(c.Request.Context()); e != nil {
				driverError(c, 503, "vehicle_save_failed", "Vehicle changes could not be saved; refresh before retrying")
				return
			}
		}
		original.WriteHeader(buffer.Status())
		_, _ = original.Write(buffer.body.Bytes())
	}
}
func inactiveVehicleOnly(c *gin.Context) bool {
	active, ok := c.Get("selectedVehicleActive")
	return ok && !active.(bool) && !c.GetBool("sharedDriverEvidence")
}
func vehicleLimit() int {
	n, e := strconv.Atoi(os.Getenv("DRIVER_MAX_VEHICLES"))
	if e == nil && n >= 1 && n <= 5 {
		return n
	}
	return 5
}
func fleetOwner(c *gin.Context) string {
	if s := c.Param("driverID"); s != "" {
		return s
	}
	return driverID(c)
}
func readFleet(c *gin.Context, p *pgxpool.Pool) {
	rows, e := p.Query(c.Request.Context(), `SELECT id::text,is_active,make,model,model_year,color,plate,review_status,approval_status,COALESCE((SELECT i.status FROM driver_inspections_records i WHERE i.vehicle_id=v.id AND i.vehicle_revision=v.inspection_revision ORDER BY i.created_at DESC,i.id DESC LIMIT 1),'required'),inspection_revision FROM driver_vehicle_records v WHERE driver_id=$1::uuid ORDER BY is_active DESC,updated_at,id`, fleetOwner(c))
	if e != nil {
		reviewFailure(c)
		return
	}
	defer rows.Close()
	items := []gin.H{}
	for rows.Next() {
		var id, makeName, model, color, plate, review, approval, inspection string
		var year *int
		var active bool
		var rev int64
		if e = rows.Scan(&id, &active, &makeName, &model, &year, &color, &plate, &review, &approval, &inspection, &rev); e != nil {
			reviewFailure(c)
			return
		}
		items = append(items, gin.H{"vehicleId": id, "isActive": active, "make": makeName, "model": model, "year": year, "color": color, "plate": plate, "reviewStatus": review, "approvalStatus": approval, "inspectionStatus": inspection, "vehicleRevision": rev})
	}
	if rows.Err() != nil {
		reviewFailure(c)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, contracts.APIResponse{Data: gin.H{"vehicles": items, "limit": vehicleLimit()}})
}
func (a *driverAPI) vehicles(c *gin.Context) { readFleet(c, a.pool) }
func (a *adminAPI) vehicles(c *gin.Context)  { readFleet(c, a.pool) }
func (a *driverAPI) addVehicle(c *gin.Context) {
	var b struct {
		Make  string `json:"make"`
		Model string `json:"model"`
		Year  int    `json:"year"`
		Color string `json:"color"`
		Plate string `json:"plate"`
	}
	if c.ShouldBindJSON(&b) != nil || strings.TrimSpace(b.Make) == "" || strings.TrimSpace(b.Model) == "" || strings.TrimSpace(b.Plate) == "" || len(b.Make) > 100 || len(b.Model) > 100 || len(b.Color) > 100 || len(b.Plate) > 30 || b.Year < 1900 || b.Year > time.Now().Year()+1 {
		driverError(c, 400, "invalid_vehicle", "Make, model, year and plate are required")
		return
	}
	tx, e := a.pool.Begin(c.Request.Context())
	if e != nil {
		reviewFailure(c)
		return
	}
	defer tx.Rollback(context.Background())
	owner := driverID(c)
	if _, e = tx.Exec(c.Request.Context(), `SELECT id FROM users WHERE id=$1::uuid FOR UPDATE`, owner); e != nil {
		reviewFailure(c)
		return
	}
	if _, e = tx.Exec(c.Request.Context(), `INSERT INTO driver_profiles(driver_id)VALUES($1::uuid)ON CONFLICT DO NOTHING`, owner); e != nil {
		reviewFailure(c)
		return
	}
	var count int
	var duplicate, approved bool
	e = tx.QueryRow(c.Request.Context(), `SELECT count(*),COALESCE(bool_or(upper(regexp_replace(plate,'[^a-zA-Z0-9]','','g'))=upper(regexp_replace($2,'[^a-zA-Z0-9]','','g'))),FALSE),COALESCE(bool_or(approval_status='approved'),FALSE) FROM driver_vehicle_records WHERE driver_id=$1::uuid`, owner, b.Plate).Scan(&count, &duplicate, &approved)
	if e != nil {
		reviewFailure(c)
		return
	}
	if duplicate {
		driverError(c, 409, "vehicle_exists", "This registration plate is already in your vehicles")
		return
	}
	if count > 0 && !approved {
		driverError(c, 403, "first_vehicle_approval_required", "Your first car must be approved before adding another")
		return
	}
	if count >= vehicleLimit() {
		driverError(c, 409, "vehicle_limit", "Your vehicle limit has been reached")
		return
	}
	id := uuid.NewString()
	_, e = tx.Exec(c.Request.Context(), `INSERT INTO driver_vehicle_records(id,driver_id,is_active,make,model,model_year,color,plate)VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8)`, id, owner, count == 0, strings.TrimSpace(b.Make), strings.TrimSpace(b.Model), b.Year, strings.TrimSpace(b.Color), strings.TrimSpace(b.Plate))
	if e == nil {
		_, e = insertDriverEvent(c, tx, owner, "driver.vehicle.updated", gin.H{"vehicleId": id, "isActive": count == 0})
	}
	if e != nil || tx.Commit(c.Request.Context()) != nil {
		reviewFailure(c)
		return
	}
	c.JSON(201, contracts.APIResponse{Data: gin.H{"vehicleId": id, "isActive": count == 0}})
}
func (a *driverAPI) activateVehicle(c *gin.Context) {
	id := c.Param("vehicleID")
	if _, e := uuid.Parse(id); e != nil {
		driverError(c, 400, "invalid_vehicle_id", "Choose a vehicle")
		return
	}
	owner := driverID(c)
	tx, e := a.pool.Begin(c.Request.Context())
	if e != nil {
		reviewFailure(c)
		return
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(c.Request.Context(), `SELECT id FROM users WHERE id=$1::uuid FOR UPDATE`, owner); e != nil {
		reviewFailure(c)
		return
	}
	// Matching locks this same driver row before offering. Switching cannot race it.
	var blocked bool
	e = tx.QueryRow(c.Request.Context(), `SELECT online_requested OR available OR status<>'offline' FROM drivers WHERE id=$1::uuid FOR UPDATE`, owner).Scan(&blocked)
	if e != nil && e != pgx.ErrNoRows {
		reviewFailure(c)
		return
	}
	var busy bool
	if e = tx.QueryRow(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM driver_assignments WHERE driver_id=$1::uuid AND status='offered') OR EXISTS(SELECT 1 FROM trips WHERE assigned_driver_id=$1::uuid AND status IN ('accepted','arrived','started'))`, owner).Scan(&busy); e != nil {
		reviewFailure(c)
		return
	}
	if blocked || busy {
		driverError(c, 409, "vehicle_switch_busy", "Go offline and finish or decline your current trip or offer before switching vehicles")
		return
	}
	var active bool
	if e = tx.QueryRow(c.Request.Context(), `SELECT is_active FROM driver_vehicle_records WHERE driver_id=$1::uuid AND id=$2::uuid FOR UPDATE`, owner, id).Scan(&active); e != nil {
		driverError(c, 404, "vehicle_not_found", "Vehicle was not found")
		return
	}
	if !active {
		if _, e = tx.Exec(c.Request.Context(), `SELECT set_config('heygo.vehicle_id',$1,TRUE)`, id); e != nil {
			reviewFailure(c)
			return
		}
		var ready bool
		e = tx.QueryRow(c.Request.Context(), `SELECT COALESCE((SELECT eligible_to_drive FROM driver_eligibility WHERE driver_id=$1::uuid),FALSE) AND EXISTS(SELECT 1 FROM ride_categories c WHERE driver_category_eligible($1::uuid,c.slug))`, owner).Scan(&ready)
		if e != nil {
			reviewFailure(c)
			return
		}
		if !ready {
			driverError(c, 409, "vehicle_not_approved", "This vehicle needs current approved documents, inspection and category review before use")
			return
		}
		if _, e = tx.Exec(c.Request.Context(), `UPDATE driver_vehicle_records SET is_active=FALSE WHERE driver_id=$1::uuid AND is_active`, owner); e != nil {
			reviewFailure(c)
			return
		}
		if _, e = tx.Exec(c.Request.Context(), `UPDATE driver_vehicle_records SET is_active=TRUE WHERE id=$1::uuid`, id); e != nil {
			reviewFailure(c)
			return
		}
		if _, e = tx.Exec(c.Request.Context(), `UPDATE drivers d SET car_plate=v.plate,package_slug=v.package_slug,available=FALSE,online_requested=FALSE,status='offline',updated_at=NOW() FROM driver_vehicle_records v WHERE v.id=$2::uuid AND d.id=$1::uuid`, owner, id); e != nil {
			reviewFailure(c)
			return
		}
		if _, e = insertDriverEvent(c, tx, owner, "driver.vehicle.updated", gin.H{"vehicleId": id, "isActive": true}); e != nil {
			reviewFailure(c)
			return
		}
	}
	if tx.Commit(c.Request.Context()) != nil {
		reviewFailure(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"vehicleId": id, "isActive": true}})
}
