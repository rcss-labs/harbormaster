// Command harbormaster-mcp is the public, read-only MCP gateway in front of
// Harbormaster's private API. It holds no credential: every call is made with
// the caller's own token.
package main

import (
	"fmt"
	"io"
	"os"
)

const usageText = `usage: harbormaster-mcp <command>
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprint(stdout, usageText)
		return 0
	}
	fmt.Fprint(stderr, usageText)
	return 2
}
