// admin-user creates a HeyGo staff account after database migrations have run.
// It intentionally has no public HTTP registration counterpart.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/luxipha/heyGo_backend/shared/adminauth"
	"github.com/luxipha/heyGo_backend/shared/db"
	"golang.org/x/term"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: go run ./cmd/admin-user <username> <comma-separated roles>")
		os.Exit(2)
	}
	username := strings.ToLower(strings.TrimSpace(os.Args[1]))
	roles := strings.Split(os.Args[2], ",")
	seen := make(map[string]bool)
	for _, role := range roles {
		switch role {
		case "operations", "compliance", "support", "finance", "safety", "super_admin":
		default:
			fmt.Fprintln(os.Stderr, "invalid staff role:", role)
			os.Exit(2)
		}
		if seen[role] {
			fmt.Fprintln(os.Stderr, "duplicate staff role:", role)
			os.Exit(2)
		}
		seen[role] = true
	}
	if len(username) < 3 || len(username) > 100 || strings.ContainsAny(username, " \t\r\n") {
		fmt.Fprintln(os.Stderr, "username must be 3-100 characters without spaces")
		os.Exit(2)
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required")
		os.Exit(2)
	}
	fmt.Fprint(os.Stderr, "Staff password: ")
	var password []byte
	var err error
	if term.IsTerminal(int(os.Stdin.Fd())) {
		password, err = term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
	} else {
		var line string
		line, err = bufio.NewReader(os.Stdin).ReadString('\n')
		password = []byte(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to read password:", err)
		os.Exit(1)
	}
	hash, err := adminauth.HashPassword(string(password))
	for i := range password {
		password[i] = 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := db.NewPostgresPool(ctx, db.Config{URL: databaseURL, MaxConnIdleTime: time.Minute, MaxConns: 2, MinConns: 0})
	if err != nil {
		fmt.Fprintln(os.Stderr, "database unavailable:", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err = db.Migrate(ctx, pool); err != nil {
		fmt.Fprintln(os.Stderr, "migration failed:", err)
		os.Exit(1)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "staff account was not created:", err)
		os.Exit(1)
	}
	defer tx.Rollback(ctx)
	id := uuid.New()
	_, err = tx.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash,role) VALUES($1,$2,$3,$4)`, id, username, hash, roles[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "staff account was not created:", err)
		os.Exit(1)
	}
	for _, role := range roles {
		if _, err = tx.Exec(ctx, `INSERT INTO admin_user_roles(admin_id,role) VALUES($1,$2)`, id, role); err != nil {
			fmt.Fprintln(os.Stderr, "staff account was not created:", err)
			os.Exit(1)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "staff account was not created:", err)
		os.Exit(1)
	}
	fmt.Println("Created staff account:", username, "roles:", strings.Join(roles, ","))
}
