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

	"github.com/cprakhar/uber-clone/shared/adminauth"
	"github.com/cprakhar/uber-clone/shared/db"
	"github.com/google/uuid"
	"golang.org/x/term"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./cmd/admin-user <username>")
		os.Exit(2)
	}
	username := strings.ToLower(strings.TrimSpace(os.Args[1]))
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
	_, err = pool.Exec(ctx, `INSERT INTO admin_users(id,username,password_hash) VALUES($1,$2,$3)`, uuid.New(), username, hash)
	if err != nil {
		fmt.Fprintln(os.Stderr, "staff account was not created:", err)
		os.Exit(1)
	}
	fmt.Println("Created staff account:", username)
}
