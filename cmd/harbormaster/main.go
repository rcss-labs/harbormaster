// Command harbormaster serves the Harbormaster API and embedded web UI.
package main

import (
	"fmt"
	"io"
	"os"
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

var stubCommands = map[string]bool{
	"serve":        true,
	"migrate":      true,
	"healthcheck":  true,
	"create-admin": true,
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}

	command := args[0]
	switch {
	case command == "version":
		printVersion(stdout, version, commit)
		return 0
	case stubCommands[command]:
		fmt.Fprintf(stderr, "%s: not implemented yet\n", command)
		return 1
	default:
		fmt.Fprint(stderr, usageText)
		return 2
	}
}
