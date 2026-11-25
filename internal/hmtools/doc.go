// Package hmtools is the read-only tool table of Harbormaster's MCP tools:
// each tool's name, schema and the single API route it may call, plus the
// upstream client that makes that one call with the caller's own bearer token.
//
// evidence.go is the rule table that says, for each tool, which service an answer belongs to and which action
// (View or Logs) reads it. Code decides this from the tool call, never the model. An answer is attributed in one of
// three ways: wholly to the service in the call's argument; element by element from one top-level array (each
// element names its own service); or element by element from several arrays nested in the answer, such as the
// per-service advisor rows of the cost analytics. Totals, series and KPIs in such an answer are not citable. A nested row may name a service that has been deleted;
// whoever reads the evidence later decides who may see it (admins only).
//
// It is shared by the MCP gateway (internal/mcpgw) and the agent service, so it
// imports nothing from this module except internal/apitoken, and only the
// standard library beyond that. Its upstream.go holds the one HTTP client.
package hmtools
