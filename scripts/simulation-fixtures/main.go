// simulation-fixtures adds synthetic records only to the explicitly guarded local database.
// It never resets a database, deletes records, issues auth tokens, or fakes socket presence.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/luxipha/heyGo_backend/shared/geo"
)

const prefix = "heygo-simulation:v1:"
const market = "SIM_LAGOS"
const driverCount = 25

func fixtureID(label string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(prefix+label)).String()
}

func guard(raw, project, emulator string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() != "127.0.0.1" || u.Port() != "55439" || u.Path != "/heygo_rider_staging" || u.User == nil || u.User.Username() == "" {
		return errors.New("only the named loopback simulation database on port 55439 is allowed")
	}
	for k := range u.Query() {
		if k != "sslmode" && k != "connect_timeout" {
			return errors.New("database connection overrides are forbidden")
		}
	}
	if project != "heygo-admin-local" || emulator != "127.0.0.1:8086" {
		return errors.New("local project and Pub/Sub emulator are required")
	}
	return nil
}
func main() {
	apply := flag.Bool("apply", false, "commit additive fixtures; default validates then rolls back")
	demo := flag.Bool("routing-demo", false, "configure the previously approved OSRM public demo for this local database only")
	flag.Parse()
	if err := run(*apply, *demo); err != nil {
		fmt.Fprintln(os.Stderr, "Simulation fixtures failed:", err)
		os.Exit(1)
	}
}
func run(apply, demo bool) error {
	raw := os.Getenv("DATABASE_URL")
	if err := guard(raw, os.Getenv("GCP_PROJECT_ID"), os.Getenv("PUBSUB_EMULATOR_HOST")); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, raw)
	if err != nil {
		return errors.New("could not configure local database")
	}
	defer pool.Close()
	return seed(ctx, pool, apply, demo)
}
func seed(ctx context.Context, pool *pgxpool.Pool, apply, demo bool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(25102501)`); err != nil {
		return err
	}
	exec := func(sql string, args ...any) error { _, e := tx.Exec(ctx, sql, args...); return e }
	maker, checker := fixtureID("maker"), fixtureID("checker")
	for _, a := range []struct{ id, name string }{{maker, "simulation-fixture-maker"}, {checker, "simulation-fixture-checker"}} {
		var collision bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin_users WHERE (id=$1::uuid OR username=$2) AND NOT(id=$1::uuid AND username=$2 AND active=FALSE AND password_hash='!simulation-login-disabled'))`, a.id, a.name).Scan(&collision); err != nil {
			return err
		}
		if collision {
			return errors.New("fixture staff identity collision; existing records preserved")
		}
		if err = exec(`INSERT INTO admin_users(id,username,password_hash,active,role) VALUES($1::uuid,$2,'!simulation-login-disabled',FALSE,'operations') ON CONFLICT(id) DO NOTHING`, a.id, a.name); err != nil {
			return err
		}
	}
	// Refuse overlapping pre-existing market coverage rather than making routing ambiguous.
	boundary := `MULTIPOLYGON(((3.20 6.40,3.60 6.40,3.60 6.70,3.20 6.70,3.20 6.40)))`
	var collision bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM trip_geofences WHERE kind='market' AND status='approved' AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW()) AND id<>$1::uuid AND ST_Intersects(boundary,ST_GeomFromText($2,4326)))`, fixtureID("market"), boundary).Scan(&collision); err != nil {
		return err
	}
	if collision {
		return errors.New("existing market overlaps the synthetic Lagos footprint; no changes committed")
	}
	if err = exec(`INSERT INTO trip_geofences(id,kind,code,name,version,boundary,status,effective_from,created_by,approved_by,approved_at) VALUES($1::uuid,'market',$2,'SIMULATION ONLY - Lagos metro test footprint',1,ST_GeomFromText($3,4326),'approved',NOW()-INTERVAL '1 day',$4::uuid,$5::uuid,NOW()) ON CONFLICT(id) DO NOTHING`, fixtureID("market"), market, boundary, maker, checker); err != nil {
		return err
	}
	// Booking requires a unique region as well as a service market. This is
	// explicitly a synthetic test footprint, not an official Lagos boundary.
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM trip_geofences WHERE kind='region' AND status='approved' AND effective_from<=NOW() AND (effective_until IS NULL OR effective_until>NOW()) AND id<>$1::uuid AND ST_Intersects(boundary,ST_GeomFromText($2,4326)))`, fixtureID("region"), boundary).Scan(&collision); err != nil {
		return err
	}
	if collision {
		return errors.New("existing region overlaps the synthetic footprint; preserved")
	}
	if err = exec(`INSERT INTO trip_geofences(id,kind,code,name,version,boundary,status,effective_from,created_by,approved_by,approved_at) VALUES($1::uuid,'region','SIM_LAGOS_REGION','SIMULATION ONLY - test region',1,ST_GeomFromText($2,4326),'approved',NOW()-INTERVAL '1 day',$3::uuid,$4::uuid,NOW()) ON CONFLICT(id) DO NOTHING`, fixtureID("region"), boundary, maker, checker); err != nil {
		return err
	}
	if err = exec(`INSERT INTO pricing_versions(id,market_code,version,status,effective_from,created_by,approved_by,approved_at) VALUES($1::uuid,$2,1,'approved',NOW()-INTERVAL '1 day',$3::uuid,$4::uuid,NOW()) ON CONFLICT(id) DO NOTHING`, fixtureID("pricing"), market, maker, checker); err != nil {
		return err
	}
	if err = exec(`INSERT INTO pricing_package_rates(pricing_id,package_slug,base_kobo,per_km_kobo,per_minute_kobo) SELECT $1::uuid,'sedan',100000,7500,2500 WHERE NOT EXISTS(SELECT 1 FROM pricing_package_rates WHERE pricing_id=$1::uuid AND package_slug='sedan')`, fixtureID("pricing")); err != nil {
		return err
	}
	if err = exec(`INSERT INTO operating_balance_policies(id,market_code,version,minimum_kobo,warning_kobo,allow_negative,maximum_negative_kobo,status,effective_from,created_by,approved_by,approved_at) VALUES($1::uuid,$2,1,0,0,FALSE,0,'approved',NOW()-INTERVAL '1 day',$3::uuid,$4::uuid,NOW()) ON CONFLICT(id) DO NOTHING`, fixtureID("balance-policy"), market, maker, checker); err != nil {
		return err
	}
	if demo {
		settings := geo.Settings{RouteProvider: "osrm", OSRMEndpoint: "https://router.project-osrm.org/route/v1/driving", AllowDemo: true, PlaceProvider: "google", MapProvider: "google", NavigationProvider: "google"}
		if err = settings.Validate(); err != nil {
			return err
		}
		raw, _ := json.Marshal(settings)
		var previous []byte
		err = tx.QueryRow(ctx, `SELECT configuration FROM admin_geo_settings WHERE id=1 FOR UPDATE`).Scan(&previous)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			var existing geo.Settings
			if json.Unmarshal(previous, &existing) != nil || existing != settings {
				return errors.New("existing Admin geo settings differ; preserved unchanged")
			}
		} else {
			if err = exec(`INSERT INTO admin_geo_settings(id,configuration,version,updated_by) VALUES(1,$1::jsonb,1,$2::uuid)`, string(raw), checker); err != nil {
				return err
			}
			if err = exec(`INSERT INTO admin_audit(id,admin_id,action,details) VALUES($1::uuid,$2::uuid,'geo.settings.updated',jsonb_build_object('settings',$3::jsonb,'reason','Local simulation: restore previously approved OSRM demo; no production SLA','source','simulation-fixtures-v1')) ON CONFLICT(id) DO NOTHING`, fixtureID("geo-audit"), checker, string(raw)); err != nil {
				return err
			}
		}
	}
	for n := 1; n <= driverCount; n++ {
		label := fmt.Sprintf("driver-%02d", n)
		id := fixtureID(label)
		owner := prefix + label
		email := fmt.Sprintf("sim-driver-%02d@simulation.heygo.invalid", n)
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE (id=$1::uuid OR lower(email)=lower($2) OR casper_id_user_id=$3) AND NOT(id=$1::uuid AND casper_id_user_id=$3 AND email=$2))`, id, email, owner).Scan(&collision); err != nil {
			return err
		}
		if collision {
			return errors.New("fixture driver identity collision; existing records preserved")
		}
		if err = exec(`INSERT INTO users(id,casper_id_user_id,human_id,roles,verified,email,email_verified,active) VALUES($1::uuid,$2,$2,ARRAY['driver'],TRUE,$3,TRUE,TRUE) ON CONFLICT(id) DO NOTHING`, id, owner, email); err != nil {
			return err
		}
		if err = exec(`INSERT INTO driver_profiles(driver_id,display_name,manual_verification_status,admin_status,admin_reason,admin_reviewed_at) VALUES($1::uuid,$2,'approved','approved','Synthetic local fixture; not real identity/inspection evidence',NOW()) ON CONFLICT(driver_id) DO NOTHING`, id, fmt.Sprintf("Simulation Driver %02d", n)); err != nil {
			return err
		}
		if err = exec(`INSERT INTO driver_vehicle_records(driver_id,package_slug,make,model,model_year,color,plate,review_status,review_reason,reviewed_at,passenger_seats,working_ac) VALUES($1::uuid,'sedan','SIMULATION','Test Sedan',2020,'Test green',$2,'approved','Synthetic local fixture only',NOW(),4,TRUE) ON CONFLICT(driver_id) WHERE is_active DO NOTHING`, id, fmt.Sprintf("SIM-%03d-TEST", n)); err != nil {
			return err
		}
		for _, kind := range []string{"driver_license", "vehicle_license", "roadworthiness", "auto_insurance", "hackney_permit"} {
			if err = exec(`INSERT INTO driver_documents_records(id,driver_id,type,status,storage_key,metadata,reviewed_at,reviewed_by,expires_at) VALUES($1::uuid,$2::uuid,$3,'approved',$4,'{"synthetic":true,"source":"simulation-fixtures-v1","notRealEvidence":true}',NOW(),$5::uuid,NOW()+INTERVAL '30 days') ON CONFLICT(id) DO NOTHING`, fixtureID(label+":"+kind), id, kind, "simulation-only/not-uploaded/"+label+"/"+kind, checker); err != nil {
				return err
			}
		}
		// Runtime fixture, not a claim that physical inspection/media review occurred.
		if err = exec(`INSERT INTO driver_inspections_records(id,driver_id,partner_name,partner_reference,status,outcome_reason,reviewed_by,inspected_at,vehicle_revision,vehicle_snapshot) VALUES($1::uuid,$2::uuid,'SIMULATION ONLY',$3,'completed','Synthetic runtime eligibility fixture; no physical inspection or uploaded media',$4::uuid,NOW(),0,'{"synthetic":true,"notRealEvidence":true}') ON CONFLICT(id) DO NOTHING`, fixtureID(label+":inspection"), id, owner, checker); err != nil {
			return err
		}
		if err = exec(`UPDATE driver_vehicles SET approval_status='approved',approved_at=NOW() WHERE driver_id=$1::uuid`, id); err != nil {
			return err
		}

		if err = exec(`INSERT INTO admin_audit(id,admin_id,action,target_driver_id,details) VALUES($1::uuid,$2::uuid,'simulation.fixture.created',$3::uuid,'{"synthetic":true,"source":"simulation-fixtures-v1","approvalEvidence":"fixture-only"}') ON CONFLICT(id) DO NOTHING`, fixtureID(label+":audit"), checker, id); err != nil {
			return err
		}
	}
	// Verify real runtime eligibility functions without setting online/presence or funding money.
	var eligible, category, balance int
	if err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE e.eligible_to_drive),count(*) FILTER(WHERE driver_category_eligible(u.id,'sedan')),count(*) FILTER(WHERE driver_balance_eligible(u.id,$1)) FROM users u JOIN driver_eligibility e ON e.driver_id=u.id WHERE u.casper_id_user_id LIKE $2`, market, prefix+"driver-%").Scan(&eligible, &category, &balance); err != nil {
		return err
	}
	if eligible != driverCount || category != driverCount || balance != driverCount {
		return fmt.Errorf("eligibility validation failed: runtime=%d category=%d balance=%d; no changes committed", eligible, category, balance)
	}
	if err = validateMarket(ctx, tx); err != nil {
		return err
	}
	if apply {
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"committed": apply, "drivers": driverCount, "eligible": eligible, "categoryEligible": category, "balanceEligible": balance, "market": market, "onlinePresenceFaked": false, "syntheticRatesOnly": true, "physicalInspectionProven": false})
}
func validateMarket(ctx context.Context, tx pgx.Tx) error {
	var actual string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(driver_market_at(ST_SetSRID(ST_MakePoint(3.421,6.428),4326)::geography),'')`).Scan(&actual); err != nil {
		return err
	}
	if actual != market {
		return errors.New("test pickup does not resolve uniquely to synthetic Lagos market")
	}
	return nil
}
