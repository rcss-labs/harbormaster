// Package hmtools is the read-only tool table of Harbormaster's MCP tools:
// each tool's name, schema and the single API route it may call, plus the
// upstream client that makes that one call with the caller's own bearer token.
//
// It is shared by the MCP gateway (internal/mcpgw) and the agent service, so it
// imports nothing from this module except internal/apitoken, and only the
// standard library beyond that. Its upstream.go holds the one HTTP client.
package hmtools
