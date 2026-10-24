package handler

import (
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/luxipha/heyGo_backend/shared/contracts"
	"net/http"
	"strings"
	"time"
)

type categoryRecord struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Passengers  int    `json:"passengers"`
	MinimumYear int    `json:"minimumYear"`
	RequiresAC  bool   `json:"requiresAc"`
	Status      string `json:"status"`
	Legacy      bool   `json:"legacy"`
	Revision    int64  `json:"revision"`
	EditedBy    string `json:"editedBy"`
}
type catalogueModel struct {
	ID          string   `json:"id"`
	Make        string   `json:"make"`
	Model       string   `json:"model"`
	Variant     string   `json:"variant"`
	YearFrom    int      `json:"yearFrom"`
	YearTo      int      `json:"yearTo"`
	BodyType    string   `json:"bodyType"`
	CategoryIDs []string `json:"categoryIds"`
	Status      string   `json:"status"`
	Revision    int64    `json:"revision"`
}

func registerAdminCatalogueRoutes(g *gin.RouterGroup, a *adminAPI) {
	g.GET("/catalogue", a.readCatalogue)
	g.GET("/drivers/:driverID/catalogue", a.readCatalogue)
	g.POST("/drivers/:driverID/catalogue/vehicles", a.requireCSRF, a.saveCatalogueVehicles)
	g.POST("/catalogue/categories", a.requireCSRF, a.saveCategory)
	g.POST("/catalogue/vehicles", a.requireCSRF, a.saveCatalogueVehicles)
	g.POST("/catalogue/categories/:id/status", a.requireCSRF, a.categoryStatus)
	g.POST("/catalogue/vehicles/:id/status", a.requireCSRF, a.catalogueVehicleStatus)
}
func categoryValidation(b categoryRecord) string {
	if strings.TrimSpace(b.Name) == "" || len(b.Name) > 80 || len(b.Description) > 300 || b.Passengers < 1 || b.Passengers > 100 || b.MinimumYear < 1900 || b.MinimumYear > time.Now().Year()+1 {
		return "Valid name, passenger capacity and minimum year are required"
	}
	return ""
}
func modelValidation(b catalogueModel) string {
	if strings.TrimSpace(b.Make) == "" || strings.TrimSpace(b.Model) == "" || len(b.Make) > 100 || len(b.Model) > 100 || len(b.Variant) > 100 || b.YearFrom < 1900 || b.YearTo < b.YearFrom || b.YearTo > time.Now().Year()+1 {
		return "Valid make, model and year range are required"
	}
	switch b.BodyType {
	case "Sedan", "Hatchback", "Wagon", "Crossover", "SUV", "MPV", "Passenger van":
	default:
		return "Choose a supported body type"
	}
	return ""
}
func catalogueDBError(c *gin.Context, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		driverError(c, 409, "catalogue_changed", "This record changed. Refresh before saving")
	} else {
		driverError(c, 409, "catalogue_conflict", "Could not save. Check duplicates, overlapping years and selected records")
	}
}
func (a *adminAPI) readCatalogue(c *gin.Context) {
	// One statement gives a consistent snapshot of models and their assignments.
	var data json.RawMessage
	err := a.pool.QueryRow(c.Request.Context(), `SELECT jsonb_build_object('categories',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',id,'slug',slug,'name',name,'description',description,'passengers',passengers,'minimumYear',minimum_year,'requiresAc',requires_ac,'status',status,'legacy',legacy,'revision',revision,'editedBy',edited_by) ORDER BY name) FROM ride_categories),'[]'), 'vehicles',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',m.id,'make',m.make,'model',m.model,'variant',m.variant,'yearFrom',m.year_from,'yearTo',m.year_to,'bodyType',m.body_type,'status',m.status,'revision',m.revision,'categoryIds',COALESCE((SELECT jsonb_agg(category_id) FROM vehicle_category_models WHERE model_id=m.id),'[]')) ORDER BY m.make,m.model) FROM vehicle_catalogue m),'[]'))`).Scan(&data)
	if err != nil {
		reviewFailure(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: data})
}
func (a *adminAPI) saveCategory(c *gin.Context) {
	var b struct {
		categoryRecord
		ModelIDs []string `json:"modelIds"`
	}
	if c.ShouldBindJSON(&b) != nil {
		driverError(c, 400, "invalid_category", "Valid category required")
		return
	}
	if msg := categoryValidation(b.categoryRecord); msg != "" {
		driverError(c, 400, "invalid_category", msg)
		return
	}
	if _, err := uuid.Parse(b.ID); err != nil {
		driverError(c, 400, "invalid_category", "Valid category ID required")
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		reviewFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	// All catalogue edits share the same lock: membership edits cannot race publication.
	if _, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(hashtext('vehicle-catalogue'))`); err != nil {
		reviewFailure(c)
		return
	}
	var status string
	var revision int64
	err = tx.QueryRow(c.Request.Context(), `SELECT status,revision FROM ride_categories WHERE id=$1::UUID FOR UPDATE`, b.ID).Scan(&status, &revision)
	if errors.Is(err, pgx.ErrNoRows) && b.Revision == 0 {
		_, err = tx.Exec(c.Request.Context(), `INSERT INTO ride_categories(id,slug,name,description,passengers,minimum_year,requires_ac,edited_by) VALUES($1::UUID,$2,$3,$4,$5,$6,$7,$8::UUID)`, b.ID, "ride-"+b.ID, strings.TrimSpace(b.Name), strings.TrimSpace(b.Description), b.Passengers, b.MinimumYear, b.RequiresAC, currentAdmin(c).ID)
	} else if err == nil && revision == b.Revision && (status == "draft" || status == "inactive") {
		_, err = tx.Exec(c.Request.Context(), `UPDATE ride_categories SET name=$2,description=$3,passengers=$4,minimum_year=$5,requires_ac=$6,legacy=FALSE,edited_by=$7::UUID,approved_by=NULL,revision=revision+1,updated_at=NOW() WHERE id=$1::UUID`, b.ID, strings.TrimSpace(b.Name), strings.TrimSpace(b.Description), b.Passengers, b.MinimumYear, b.RequiresAC, currentAdmin(c).ID)
	} else {
		driverError(c, 409, "category_locked", "Refresh this record. Only draft or inactive categories can be edited")
		return
	}
	if err != nil {
		catalogueDBError(c, err)
		return
	}
	// Changing associations changes the category revision and model revisions too.
	if _, err = tx.Exec(c.Request.Context(), `UPDATE vehicle_catalogue SET revision=revision+1 WHERE id IN(SELECT model_id FROM vehicle_category_models WHERE category_id=$1::UUID) OR id=ANY($2::UUID[])`, b.ID, b.ModelIDs); err != nil {
		catalogueDBError(c, err)
		return
	}
	if _, err = tx.Exec(c.Request.Context(), `DELETE FROM vehicle_category_models WHERE category_id=$1::UUID`, b.ID); err != nil {
		reviewFailure(c)
		return
	}
	for _, id := range b.ModelIDs {
		if _, err = uuid.Parse(id); err != nil {
			driverError(c, 400, "invalid_model", "Valid model IDs required")
			return
		}
		if tag, e := tx.Exec(c.Request.Context(), `INSERT INTO vehicle_category_models(category_id,model_id) SELECT $1::UUID,id FROM vehicle_catalogue WHERE id=$2::UUID AND status<>'archived'`, b.ID, id); e != nil || tag.RowsAffected() != 1 {
			catalogueDBError(c, e)
			return
		}
	}
	if adminAudit(c, tx, "catalogue.category.saved", "", gin.H{"categoryId": b.ID}) != nil || tx.Commit(c.Request.Context()) != nil {
		reviewFailure(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"id": b.ID}})
}
func (a *adminAPI) saveCatalogueVehicles(c *gin.Context) {
	var b struct {
		Vehicles []catalogueModel `json:"vehicles"`
	}
	if c.ShouldBindJSON(&b) != nil || len(b.Vehicles) < 1 || len(b.Vehicles) > 1000 {
		driverError(c, 400, "invalid_models", "Submit between 1 and 1000 models")
		return
	}
	if strings.HasPrefix(c.FullPath(), "/admin/drivers/") {
		if len(b.Vehicles) != 1 || b.Vehicles[0].Revision != 0 || len(b.Vehicles[0].CategoryIDs) != 0 {
			driverError(c, 400, "new_model_only", "Inspection may add one unclassified model draft")
			return
		}
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		reviewFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	if _, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(hashtext('vehicle-catalogue'))`); err != nil {
		reviewFailure(c)
		return
	}
	for _, m := range b.Vehicles {
		if m.CategoryIDs == nil {
			m.CategoryIDs = []string{}
		}
		if msg := modelValidation(m); msg != "" {
			driverError(c, 400, "invalid_model", msg)
			return
		}
		if _, err = uuid.Parse(m.ID); err != nil {
			driverError(c, 400, "invalid_model", "Valid model ID required")
			return
		}
		// Avoid ambiguous year/variant choices, not just exact duplicate ranges.
		var conflict bool
		err = tx.QueryRow(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM vehicle_catalogue WHERE id<>$1::UUID AND lower(trim(make))=lower(trim($2)) AND lower(trim(model))=lower(trim($3)) AND lower(trim(variant))=lower(trim($4)) AND year_from<=$6 AND year_to>=$5 AND status<>'archived')`, m.ID, m.Make, m.Model, m.Variant, m.YearFrom, m.YearTo).Scan(&conflict)
		if err != nil {
			reviewFailure(c)
			return
		}
		if conflict {
			driverError(c, 409, "overlapping_model", "This make, model and variant has overlapping years")
			return
		}
		// Active/pending categories cannot have their eligibility changed through a model edit.
		var locked bool
		err = tx.QueryRow(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM ride_categories c WHERE c.status IN ('active','pending','archived') AND ((c.id=ANY($2::UUID[])) IS DISTINCT FROM EXISTS(SELECT 1 FROM vehicle_category_models cm WHERE cm.model_id=$1::UUID AND cm.category_id=c.id)))`, m.ID, m.CategoryIDs).Scan(&locked)
		if err != nil {
			catalogueDBError(c, err)
			return
		}
		if locked {
			driverError(c, 409, "category_locked", "Deactivate the category before changing its eligible models")
			return
		}
		var tag pgconn.CommandTag
		var e error
		if m.Revision == 0 {
			tag, e = tx.Exec(c.Request.Context(), `INSERT INTO vehicle_catalogue(id,make,model,variant,year_from,year_to,body_type) VALUES($1::UUID,$2,$3,$4,$5,$6,$7)`, m.ID, strings.TrimSpace(m.Make), strings.TrimSpace(m.Model), strings.TrimSpace(m.Variant), m.YearFrom, m.YearTo, m.BodyType)
		} else {
			tag, e = tx.Exec(c.Request.Context(), `UPDATE vehicle_catalogue SET make=$2,model=$3,variant=$4,year_from=$5,year_to=$6,body_type=$7,revision=revision+1,updated_at=NOW() WHERE id=$1::UUID AND revision=$8 AND status='draft'`, m.ID, strings.TrimSpace(m.Make), strings.TrimSpace(m.Model), strings.TrimSpace(m.Variant), m.YearFrom, m.YearTo, m.BodyType, m.Revision)
		}
		if e != nil {
			catalogueDBError(c, e)
			return
		}
		if tag.RowsAffected() != 1 {
			catalogueDBError(c, pgx.ErrNoRows)
			return
		}
		if _, err = tx.Exec(c.Request.Context(), `UPDATE ride_categories SET revision=revision+1,edited_by=$2::UUID WHERE id IN(SELECT category_id FROM vehicle_category_models WHERE model_id=$1::UUID) OR id=ANY($3::UUID[])`, m.ID, currentAdmin(c).ID, m.CategoryIDs); err != nil {
			catalogueDBError(c, err)
			return
		}
		if _, err = tx.Exec(c.Request.Context(), `DELETE FROM vehicle_category_models WHERE model_id=$1::UUID`, m.ID); err != nil {
			reviewFailure(c)
			return
		}
		for _, id := range m.CategoryIDs {
			if _, err = tx.Exec(c.Request.Context(), `INSERT INTO vehicle_category_models(category_id,model_id) VALUES($1::UUID,$2::UUID)`, id, m.ID); err != nil {
				catalogueDBError(c, err)
				return
			}
		}
	}
	if adminAudit(c, tx, "catalogue.models.saved", "", gin.H{"count": len(b.Vehicles)}) != nil || tx.Commit(c.Request.Context()) != nil {
		reviewFailure(c)
		return
	}
	c.JSON(http.StatusOK, contracts.APIResponse{Data: gin.H{"saved": len(b.Vehicles)}})
}
func (a *adminAPI) categoryStatus(c *gin.Context)         { a.changeCatalogueStatus(c, true) }
func (a *adminAPI) catalogueVehicleStatus(c *gin.Context) { a.changeCatalogueStatus(c, false) }
func (a *adminAPI) changeCatalogueStatus(c *gin.Context, category bool) {
	var b struct {
		Status   string `json:"status"`
		Revision int64  `json:"revision"`
	}
	id := c.Param("id")
	if _, err := uuid.Parse(id); err != nil || c.ShouldBindJSON(&b) != nil {
		driverError(c, 400, "invalid_transition", "Valid ID and status required")
		return
	}
	tx, err := a.pool.Begin(c.Request.Context())
	if err != nil {
		reviewFailure(c)
		return
	}
	defer tx.Rollback(c.Request.Context())
	if _, err = tx.Exec(c.Request.Context(), `SELECT pg_advisory_xact_lock(hashtext('vehicle-catalogue'))`); err != nil {
		reviewFailure(c)
		return
	}
	table := "vehicle_catalogue"
	if category {
		table = "ride_categories"
	}
	var old string
	var revision int64
	err = tx.QueryRow(c.Request.Context(), `SELECT status,revision FROM `+table+` WHERE id=$1::UUID FOR UPDATE`, id).Scan(&old, &revision)
	if err != nil || revision != b.Revision {
		catalogueDBError(c, pgx.ErrNoRows)
		return
	}
	allowed := !category && ((old == "draft" && b.Status == "active") || (old == "active" && b.Status == "draft") || (old == "draft" && b.Status == "archived"))
	if category {
		allowed = ((old == "draft" || old == "inactive") && (b.Status == "pending" || b.Status == "archived")) || (old == "pending" && (b.Status == "draft" || b.Status == "active")) || (old == "active" && b.Status == "inactive")
	}
	if !allowed {
		driverError(c, 409, "invalid_transition", "This status change is not available")
		return
	}
	if category && (b.Status == "pending" || b.Status == "active") {
		var ready bool
		var editor string
		err = tx.QueryRow(c.Request.Context(), `SELECT COALESCE(edited_by::TEXT,''), (legacy OR EXISTS(SELECT 1 FROM vehicle_category_models cm JOIN vehicle_catalogue m ON m.id=cm.model_id WHERE cm.category_id=c.id AND m.status='active' AND m.year_to>=c.minimum_year)) AND EXISTS(SELECT 1 FROM pricing_package_rates r JOIN pricing_versions p ON p.id=r.pricing_id JOIN trip_geofences g ON g.code=p.market_code AND g.kind='market' WHERE r.package_slug=c.slug AND p.status='approved' AND p.effective_from<=NOW() AND (p.effective_until IS NULL OR p.effective_until>NOW()) AND g.status='approved' AND g.effective_from<=NOW() AND (g.effective_until IS NULL OR g.effective_until>NOW())) FROM ride_categories c WHERE id=$1::UUID`, id).Scan(&editor, &ready)
		if err != nil {
			reviewFailure(c)
			return
		}
		if !ready {
			driverError(c, 409, "category_not_ready", "Link current approved market pricing and at least one active eligible model first")
			return
		}
		if b.Status == "active" && editor == currentAdmin(c).ID {
			driverError(c, 409, "independent_approval_required", "A different staff member must approve publication")
			return
		}
	}
	if !category && old == "active" {
		var used bool
		err = tx.QueryRow(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM vehicle_category_models cm JOIN ride_categories c ON c.id=cm.category_id WHERE cm.model_id=$1::UUID AND c.status IN ('active','pending'))`, id).Scan(&used)
		if err != nil {
			reviewFailure(c)
			return
		}
		if used {
			driverError(c, 409, "model_in_use", "Deactivate its ride categories before changing this model")
			return
		}
	}
	if category {
		_, err = tx.Exec(c.Request.Context(), `UPDATE ride_categories SET status=$2,revision=revision+1,approved_by=CASE WHEN $2='active' THEN $3::UUID ELSE NULL END,updated_at=NOW() WHERE id=$1::UUID`, id, b.Status, currentAdmin(c).ID)
	} else {
		_, err = tx.Exec(c.Request.Context(), `UPDATE vehicle_catalogue SET status=$2,revision=revision+1,updated_at=NOW() WHERE id=$1::UUID`, id, b.Status)
	}
	if err != nil || adminAudit(c, tx, "catalogue.status.changed", "", gin.H{"id": id, "category": category, "before": old, "status": b.Status}) != nil || tx.Commit(c.Request.Context()) != nil {
		reviewFailure(c)
		return
	}
	c.JSON(200, contracts.APIResponse{Data: gin.H{"id": id, "status": b.Status}})
}
