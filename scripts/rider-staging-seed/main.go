package main

import (
	"context"
	"fmt"
	"github.com/luxipha/heyGo_backend/shared/adminauth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
	"log"
	"net/url"
	"os"
	"strings"
	"time"
)

// This seed is deliberately restricted to a named loopback staging database.
func main() {
	target := os.Getenv("RIDER_STAGING_DATABASE_URL")
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || (u.Port() != "55439" && u.Port() != "55440") || u.Path != "/heygo_rider_staging" {
		log.Fatal("Only the named loopback Rider staging database on port 55439/55440 is allowed")
	}
	raw, err := os.ReadFile(".dev/rider-staging.env")
	if err != nil {
		log.Fatal("Prepare the private staging environment first")
	}
	password := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if value, ok := strings.CutPrefix(line, "RIDER_STAGING_STAFF_PASSWORD="); ok {
			password = value
		}
	}
	if len(password) < 24 {
		log.Fatal("Private staging staff password is missing")
	}
	hash, err := adminauth.HashPassword(password)
	if err != nil {
		log.Fatal("Could not hash staging credential")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := shareddb.NewPostgresPool(ctx, shareddb.Config{URL: target, MaxConns: 2})
	if err != nil {
		log.Fatal("Could not open staging database")
	}
	defer pool.Close()

	for _, check := range []struct{ table, query string }{
		{"users", `SELECT EXISTS(SELECT 1 FROM users WHERE casper_id_user_id IS NULL OR casper_id_user_id NOT LIKE 'heygo-admin-staging:%')`},
		{"admin_users", `SELECT EXISTS(SELECT 1 FROM admin_users WHERE username<>'rider-staging-support')`},
	} {
		var exists, unsafe bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+check.table).Scan(&exists); err != nil {
			log.Fatal(err)
		}
		if exists {
			if err := pool.QueryRow(ctx, check.query).Scan(&unsafe); err != nil || unsafe {
				log.Fatal("Staging seed refused: database contains unrelated identities")
			}
		}
	}
	if err := shareddb.Migrate(ctx, pool); err != nil {
		log.Fatal(err)
	}
	fixture, err := os.ReadFile("scripts/seed-admin-staging.sql")
	if err != nil {
		log.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "SET heygo.staging_seed='enabled';\n"+strings.Replace(string(fixture), "\\set ON_ERROR_STOP on\n", "", 1)); err != nil {
		log.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer tx.Rollback(ctx)
	const staff = "a1100000-0000-4000-8000-000000000004"
	if _, err = tx.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash,role) VALUES($1::UUID,'rider-staging-support',$2,'support') ON CONFLICT(username) DO UPDATE SET password_hash=EXCLUDED.password_hash,active=TRUE,failed_attempts=0,locked_until=NULL,role='support'`, staff, hash); err != nil {
		log.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM admin_user_roles WHERE admin_id=$1::UUID AND role NOT IN ('support','safety')`, staff); err != nil {
		log.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO admin_user_roles(admin_id,role) VALUES($1::UUID,'support'),($1::UUID,'safety') ON CONFLICT DO NOTHING`, staff); err != nil {
		log.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Seeded disposable Rider staging records and staff rider-staging-support (support, safety). Password remains in the private environment file.")
}
