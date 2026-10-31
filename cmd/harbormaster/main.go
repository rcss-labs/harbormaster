// Command harbormaster serves the Harbormaster API and embedded web UI.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// These are overridden at build time via -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "none"
)

const usageText = `usage: harbormaster <command>

commands:
  serve         run the HTTP server
  migrate       apply database migrations
  healthcheck   check that the server is healthy
  version       print version information
  create-admin  create an administrator account
`

func main() {
	// SIGINT/SIGTERM cancel the context, which makes serve shut down gracefully.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(runWith(ctx, os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWith(context.Background(), args, os.Getenv, os.Stdin, stdout, stderr)
}

// exitCode maps a command error to a process exit status: 2 usage, 3 user exists, 1 anything else.
func exitCode(err error, stderr io.Writer) int {
	switch {
	case err == nil:
		return 0
	case errors.As(err, new(errUsage)):
		fmt.Fprintln(stderr, err)
		return 2
	case errors.Is(err, errUserExists):
		fmt.Fprintln(stderr, "error:", err)
		return 3
	default:
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
}

func runWith(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}

	command := args[0]
	switch {
	case command == "version":
		printVersion(stdout, version, commit)
		return 0
	case command == "serve":
		return exitCode(runServe(ctx, args[1:], getenv, stdout), stderr)
	case command == "migrate":
		return exitCode(runMigrate(ctx, getenv, stdout), stderr)
	case command == "healthcheck":
		return exitCode(runHealthcheck(ctx, getenv), stderr)
	case command == "create-admin":
		return exitCode(runCreateAdmin(ctx, args[1:], getenv, stdin, stdout), stderr)
	default:
		fmt.Fprint(stderr, usageText)
		return 2
	}
}
