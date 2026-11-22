// Package mcpgw is the harbormaster-mcp gateway: a stateless MCP server
// (Streamable HTTP) that turns each read-only tool call into exactly one
// request to Harbormaster's API, made with the caller's own bearer token.
//
// The gateway holds no credential and decides nothing: Harbormaster checks
// every call. What this package guarantees is that it can only ever reach
// the configured Harbormaster base URL, only on the routes in its tool table.
// Its non-test code may import, from this module, only internal/apitoken, and
// it has exactly one HTTP client (in upstream.go), which follows no redirects.
package mcpgw
