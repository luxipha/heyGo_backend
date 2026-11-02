package simulation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func identity(run, id string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("heygo-simulation:"+run+":"+id)).String()
}
func secret() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic("simulation entropy unavailable")
	}
	return hex.EncodeToString(b)
}
func digest(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }

type Actor struct {
	ID, ParticipantID, Token, SessionID string
	Config                              Participant
}

// Provision creates explicitly synthetic evidence and initial balances only for
// the run's deterministic new identity. It never edits unrelated users or trips.
func Provision(ctx context.Context, pool *pgxpool.Pool, run, participant string, c Participant) (Actor, error) {
	a := Actor{ID: identity(run, participant), ParticipantID: participant, Config: c}
	if e := c.Validate(); e != nil {
		return a, e
	}
	if c.Controller != "simulator" {
		return a, errors.New("app handoff is not connected")
	}
	tx, e := pool.Begin(ctx)
	if e != nil {
		return a, e
	}
	defer tx.Rollback(ctx)
	var linked *string
	var role string
	e = tx.QueryRow(ctx, `SELECT actor_user_id::text,role FROM simulation_participants WHERE id=$1::uuid AND simulation_run_id=$2::uuid AND is_simulated FOR UPDATE`, participant, run).Scan(&linked, &role)
	if e != nil || role != c.Role {
		return a, errors.New("participant ownership unavailable")
	}
	if linked != nil {
		return a, errors.New("participant already provisioned; preserve its previous state")
	}
	var exists bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1::uuid)`, a.ID).Scan(&exists)
	if e != nil || exists {
		return a, errors.New("synthetic identity collision; existing account preserved")
	}
	owner := "heygo-simulation:" + run + ":" + participant
	_, e = tx.Exec(ctx, `INSERT INTO users(id,casper_id_user_id,human_id,roles,verified,email,email_verified,active)VALUES($1::uuid,$2,$2,ARRAY[$3]::text[],TRUE,$4,TRUE,TRUE)`, a.ID, owner, c.Role, "sim-"+a.ID+"@simulation.heygo.invalid")
	if e != nil {
		return a, e
	}
	if c.Role == "driver" {
		if e = provisionDriver(ctx, tx, run, a); e != nil {
			return a, e
		}
	}
	a.Token = secret()
	a.SessionID = uuid.NewString()
	_, e = tx.Exec(ctx, `INSERT INTO user_sessions(id,user_id,access_token_hash,refresh_token_hash,access_expires_at,refresh_expires_at,device_name)VALUES($1::uuid,$2::uuid,$3,$4,NOW()+INTERVAL '15 minutes',NOW()+INTERVAL '16 minutes','Simulation runtime')`, a.SessionID, a.ID, digest(a.Token), digest(secret()))
	if e != nil {
		return a, e
	}
	_, e = tx.Exec(ctx, `UPDATE simulation_participants SET actor_user_id=$3::uuid,runtime_status='provisioned',updated_at=NOW() WHERE id=$1::uuid AND simulation_run_id=$2::uuid`, participant, run, a.ID)
	if e != nil {
		return a, e
	}
	return a, tx.Commit(ctx)
}
func provisionDriver(ctx context.Context, tx pgx.Tx, run string, a Actor) error {
	c := a.Config
	maker, checker := identity(run, "maker"), identity(run, "checker")
	for _, item := range []struct{ id, label string }{{maker, "maker"}, {checker, "checker"}} {
		_, e := tx.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash,active,role)VALUES($1::uuid,$2,'!simulation-login-disabled',FALSE,'operations')ON CONFLICT DO NOTHING`, item.id, "simulation-"+run+"-"+item.label)
		if e != nil {
			return e
		}
	}
	for _, staff := range []struct{ id, label string }{{maker, "maker"}, {checker, "checker"}} {
		var owned bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin_users WHERE id=$1::uuid AND username=$2 AND NOT active AND password_hash='!simulation-login-disabled')`, staff.id, "simulation-"+run+"-"+staff.label).Scan(&owned); e != nil || !owned {
			return errors.New("synthetic reviewer identity collision; existing staff preserved")
		}
	}
	slug, makeName, modelName, year, seats, ac := "", "SIMULATION", "Unclassified fixture", c.ModelYear, 4, true
	categoryIDs := c.CategoryIDs
	if len(categoryIDs) > 0 {
		e := tx.QueryRow(ctx, `SELECT slug,passengers,requires_ac FROM ride_categories WHERE id=$1::uuid`, categoryIDs[0]).Scan(&slug, &seats, &ac)
		if e != nil {
			return errors.New("category configuration missing")
		}
	}
	if len(categoryIDs) > 0 {
		if e := tx.QueryRow(ctx, `SELECT MAX(passengers),bool_or(requires_ac) FROM ride_categories WHERE id=ANY($1::uuid[])`, categoryIDs).Scan(&seats, &ac); e != nil {
			return e
		}
	}
	if c.ModelID != "" {
		if len(categoryIDs) == 0 {
			return errors.New("select inspected category memberships for the catalogue model")
		}
		if e := tx.QueryRow(ctx, `SELECT make,model FROM vehicle_catalogue WHERE id=$1::uuid AND $2 BETWEEN year_from AND year_to`, c.ModelID, year).Scan(&makeName, &modelName); e != nil {
			return errors.New("model/year configuration missing")
		}
	} else {
		// Compatibility is a stored catalogue property, not a hardcoded Go category.
		if slug == "" {
			if e := tx.QueryRow(ctx, `SELECT slug FROM ride_categories WHERE legacy AND status='active' AND passengers>=4 ORDER BY passengers,slug LIMIT 1`).Scan(&slug); e != nil {
				return errors.New("select a vehicle model/category; no legacy compatibility category available")
			}
		}
		var legacy bool
		if e := tx.QueryRow(ctx, `SELECT legacy FROM ride_categories WHERE slug=$1`, slug).Scan(&legacy); e != nil || !legacy {
			return errors.New("catalogue model required for this category")
		}
		if year == 0 {
			year = 2020
		}
	}
	_, e := tx.Exec(ctx, `INSERT INTO driver_profiles(driver_id,display_name,manual_verification_status,admin_status,admin_reason,admin_reviewed_at)VALUES($1::uuid,$2,'approved',$3,'SIMULATION fixture; not identity evidence',NOW())`, a.ID, c.Name, c.Approval)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO driver_vehicles(driver_id,package_slug,make,model,model_year,color,plate,review_status,review_reason,reviewed_at,passenger_seats,working_ac,catalogue_model_id)VALUES($1::uuid,$2,$3,$4,$5,'Simulation',$6,'approved','Synthetic fixture; no physical inspection',NOW(),$7,$8,$9::uuid)`, a.ID, slug, makeName, modelName, year, "SIM-"+a.ID[:8], seats, ac, nullable(c.ModelID))
	if e != nil {
		return e
	}
	for _, id := range categoryIDs {
		_, e = tx.Exec(ctx, `INSERT INTO driver_vehicle_categories(driver_id,category_id,reviewed_by)VALUES($1::uuid,$2::uuid,$3::uuid)`, a.ID, id, checker)
		if e != nil {
			return e
		}
	}
	for _, kind := range []string{"driver_license", "vehicle_license", "roadworthiness", "auto_insurance", "hackney_permit"} {
		_, e = tx.Exec(ctx, `INSERT INTO driver_documents(id,driver_id,type,status,storage_key,metadata,reviewed_at,reviewed_by,expires_at)VALUES($1::uuid,$2::uuid,$3,'approved',$4,jsonb_build_object('synthetic',TRUE,'notRealEvidence',TRUE,'simulationRunId',$5::text),NOW(),$6::uuid,NOW()+INTERVAL '30 days')`, identity(run, a.ParticipantID+kind), a.ID, kind, "simulation-only/not-uploaded/"+run+"/"+a.ParticipantID+"/"+kind, run, checker)
		if e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, `INSERT INTO driver_inspections(id,driver_id,partner_name,partner_reference,status,outcome_reason,reviewed_by,inspected_at,vehicle_revision,vehicle_snapshot)VALUES($1::uuid,$2::uuid,'SIMULATION ONLY',$3,'completed','Synthetic eligibility fixture, no physical evidence',$4::uuid,NOW(),0,'{"synthetic":true,"notRealEvidence":true}')`, identity(run, a.ParticipantID+":inspection"), a.ID, run, checker)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO driver_operating_accounts(driver_id,balance_kobo)VALUES($1::uuid,$2)`, a.ID, c.BalanceKobo)
	if e != nil {
		return e
	}
	if c.BalanceKobo != 0 {
		_, e = tx.Exec(ctx, `INSERT INTO driver_operating_entries(id,driver_id,kind,delta_kobo,balance_after_kobo,source_key,details)VALUES($1::uuid,$2::uuid,'reversal',$3,$3,$4,jsonb_build_object('synthetic',TRUE,'fixtureOpeningBalance',TRUE,'simulationRunId',$5::text))`, identity(run, a.ParticipantID+":opening-balance"), a.ID, c.BalanceKobo, "simulation:"+run+":"+a.ParticipantID, run)
		if e != nil {
			return fmt.Errorf("initial fixture balance: %w", e)
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE driver_vehicles SET approval_status='approved',approved_at=NOW() WHERE driver_id=$1::uuid`, a.ID); e != nil {
		return e
	}

	return nil
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
