package mcpgw

import "time"

// logSearchTimeout is the upstream timeout for search_logs: a Logs Insights
// query can take far longer than an ordinary read.
const logSearchTimeout = 90 * time.Second

// troubleshootTools are alerts, recent logs and log search.
func troubleshootTools() []Tool {
	return []Tool{
		{
			// internal/api/observe_alerts.go (getAlerts): scopeFrom (account and region filter the list).
			Name: "list_alerts", Title: "List alerts",
			Description: "The alerts active now across the estate (Harbormaster does not keep cleared alerts), each with its service or cluster, " +
				"severity, text, when it started and the change that preceded it if any." + scopedNote,
			Route:  get("/api/alerts"),
			Params: scopeParams(),
		},
		{
			// internal/api/observe_logs.go (getLogs): container, before, limit, task, q.
			Name: "get_service_logs", Title: "Get recent service logs",
			Description: "Recent log lines of one service from CloudWatch Logs, newest first, optionally for one container or task and filtered " +
				"by text. Needs the Logs permission on the service's tag (whoami says where). For a time range or across services use " +
				"search_logs." + logDataNote,
			Route: get("/api/services/{serviceId}/logs"),
			Params: []Param{
				serviceIDParam(),
				{Name: "container", Type: TypeString, In: InQuery,
					Description: "Optional container name (from get_task_definition); default is every container with logs."},
				{Name: "task", Type: TypeString, In: InQuery,
					Description: "Optional ECS task id to restrict to, from list_service_tasks."},
				{Name: "q", Type: TypeString, In: InQuery,
					Description: "Optional text the lines must contain."},
				{Name: "before", Type: TypeString, In: InQuery,
					Description: "Optional RFC 3339 timestamp: only lines before it, to page back in time."},
				{Name: "limit", Type: TypeInteger, In: InQuery,
					Description: "Most lines to return; the server caps it at its configured history size."},
			},
		},
		{
			// internal/api/log_search.go (listLogSearchServices): no parameters.
			Name: "list_log_search_services", Title: "List services whose logs you can search",
			Description: "The services whose logs the caller may search, with their ids. Pass those ids to estimate_log_search and search_logs.",
			Route:       get("/api/log-search/services"),
		},
		{
			// internal/api/log_search.go (postLogSearchEstimate): LogSearchRequest body.
			Name: "estimate_log_search", Title: "Estimate a log search",
			Description: "Before a log search over a large range or many services: an upper bound of the bytes it could scan, the cap one " +
				"search may scan and the daily search budget left. Takes the same arguments as search_logs; the query is optional here. Runs no search.",
			Route:  post("/api/log-search/estimate"),
			Params: logSearchParams(false),
		},
		{
			// internal/api/log_search.go (postLogSearch): LogSearchRequest body.
			Name: "search_logs", Title: "Search logs",
			Description: "Search the CloudWatch Logs of several services (at most 20 by default) over a time range with a Logs Insights query (only fields, filter, " +
				"parse, dedup, sort and limit stages). Call list_log_search_services for the service ids, and estimate_log_search first for " +
				"large ranges: each search is capped and counts against a daily scan budget that whoami reports." + logDataNote,
			Route:   post("/api/log-search"),
			Params:  logSearchParams(true),
			Timeout: logSearchTimeout,
		},
	}
}

// logSearchParams mirror LogSearchRequest in internal/api/log_search.go.
func logSearchParams(queryRequired bool) []Param {
	return []Param{
		{Name: "service_ids", Type: TypeStringArray, In: InBody, Required: true,
			Description: "Ids of the services to search (at most 20 by default), from list_log_search_services."},
		{Name: "range", Type: TypeString, In: InBody, Enum: []string{"15m", "1h", "24h", "custom"}, Default: "1h",
			Description: "Time range ending now: 15m, 1h (default) or 24h; or custom with from and to."},
		{Name: "from", Type: TypeString, In: InBody,
			Description: "Start of a custom range, RFC 3339 (for example 2026-01-02T15:04:05Z). Only with range custom."},
		{Name: "to", Type: TypeString, In: InBody,
			Description: "End of a custom range, RFC 3339, not in the future. Only with range custom; the server limits the span (7 days by default)."},
		{Name: "query", Type: TypeString, In: InBody, Required: queryRequired,
			Description: "A Logs Insights query of fields, filter, parse, dedup, sort and limit stages joined by |, " +
				"for example: filter @message like /ERROR/ | sort @timestamp desc | limit 100."},
	}
}
