// Command e2eschema recreates the dedicated Postgres schema the e2e run uses and prints
// the database URL that points the server at it, so every run starts from an empty
// database and never touches data it did not create.
//
// Usage: HM_TEST_DATABASE_URL=postgres://... go run ./tools/e2eschema
package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const schemaName = "hm_e2e"

func main() {
	if len(os.Args) > 1 {
		fmt.Fprintln(os.Stderr, "usage: HM_TEST_DATABASE_URL=postgres://... e2eschema (prints the e2e database URL)")
		os.Exit(2)
	}
	base := os.Getenv("HM_TEST_DATABASE_URL")
	if base == "" {
		fmt.Fprintln(os.Stderr, "e2eschema: HM_TEST_DATABASE_URL is not set")
		os.Exit(2)
	}
	if err := recreate(base); err != nil {
		fmt.Fprintln(os.Stderr, "e2eschema:", err)
		os.Exit(1)
	}
	parsed, err := url.Parse(base)
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2eschema: parse database url")
		os.Exit(1)
	}
	// The server-side parser reads the value with %20, not the form-encoded "+" for a space.
	options := "options=" + strings.ReplaceAll(url.QueryEscape("-c search_path="+schemaName), "+", "%20")
	if parsed.RawQuery == "" {
		parsed.RawQuery = options
	} else {
		parsed.RawQuery += "&" + options
	}
	fmt.Println(parsed.String())
}

func recreate(databaseURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect to the test database: %w", err)
	}
	defer conn.Close(ctx)
	for _, statement := range []string{
		"drop schema if exists " + schemaName + " cascade",
		"create schema " + schemaName,
	} {
		if _, err := conn.Exec(ctx, statement); err != nil {
			return fmt.Errorf("%s: %w", statement, err)
		}
	}
	return nil
}
