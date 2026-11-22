package mcpgw

// estateTools are whoami and the estate-wide reads: overview, clusters,
// services, tasks and name search. Handlers are in internal/api (see the
// comment on each entry).
func estateTools() []Tool {
	return []Tool{
		{
			// GET /api/tokens/current: the token's whoami (phase 1).
			Name: "whoami", Title: "Who am I",
			Description: "Who this connection acts as in Harbormaster: the user, whether they are an admin, the token's name, scope and expiry, " +
				"the tags the user can view and those where they may read logs (tag ids are what the tag filters take), and how much of today's " +
				"log search budget is left. Call it first when unsure what you can see.",
			Route: get("/api/tokens/current"),
		},
		{
			// internal/api/overview.go (overview): scopeFrom only.
			Name: "get_overview", Title: "Estate overview",
			Description: "A summary of the whole ECS estate: clusters, service counts by status, task health, stops per hour, stop reasons, deployments per day " +
				"and recent events. " +
				"Start here for questions like \"is anything broken?\"." + scopedNote,
			Route:  get("/api/overview"),
			Params: scopeParams(),
		},
		{
			// internal/api/clusters.go (listClusters): scopeFrom and pageFrom.
			Name: "list_clusters", Title: "List clusters",
			Description: "The ECS clusters, each with its id (pass it to get_cluster), account, region, tag and service and task counts. " +
				"Paged: pass next_cursor back as cursor for more." + scopedNote,
			Route:  get("/api/clusters"),
			Params: joinParams(scopeParams(), pageParams()),
		},
		{
			// internal/api/clusters.go (getCluster): the cluster id path value.
			Name: "get_cluster", Title: "Get cluster",
			Description: "One ECS cluster in detail, including its services and their ids (pass a service id to the service tools)." + scopedNote,
			Route:       get("/api/clusters/{clusterId}"),
			Params: []Param{
				{Name: "cluster_id", Type: TypeString, In: InPath, Required: true,
					Description: "The cluster's Harbormaster id (an opaque string), from list_clusters or search."},
			},
		},
		{
			// internal/api/services_list.go (listServicesFlat): scopeFrom only; the list is not paged (capped server-side).
			Name: "list_services", Title: "List services",
			Description: "Every ECS service in one list, each with its id (pass it to get_service and the other service tools), cluster, account, " +
				"region, tag, health, desired and running counts, task definition and newest alert. Narrow with account, region or tag on a large estate." +
				scopedNote,
			Route:  get("/api/services"),
			Params: scopeParams(),
		},
		{
			// internal/api/services.go (getService): the service id path value.
			Name: "get_service", Title: "Get service",
			Description: "One ECS service in detail: status, health, counts, deployments, load balancers and recent events." + scopedNote,
			Route:       get("/api/services/{serviceId}"),
			Params:      []Param{serviceIDParam()},
		},
		{
			// internal/api/tasks.go (listServiceTasks, tasksList): filter, q, pageFrom.
			Name: "list_service_tasks", Title: "List a service's tasks",
			Description: "The tasks of one service, with their ids, status, health, stop reasons and containers. By default only problem tasks; " +
				"set filter to all, running or stopped for others. A task id here can be passed to get_timeline_task." + scopedNote,
			Route:  get("/api/services/{serviceId}/tasks"),
			Params: joinParams([]Param{serviceIDParam()}, taskFilterParams(), pageParams()),
		},
		{
			// internal/api/tasks.go (listTasks, tasksList): scopeFrom, filter, q, pageFrom.
			Name: "list_tasks", Title: "List tasks",
			Description: "ECS tasks across the estate, with their ids, service, status, health, stop reasons and containers. By default only problem " +
				"tasks (failing, unhealthy or recently stopped with an error); set filter for others. Paged: pass next_cursor back as cursor." + scopedNote,
			Route:  get("/api/tasks"),
			Params: joinParams(scopeParams(), taskFilterParams(), pageParams()),
		},
		{
			// internal/api/search.go (search): q (at least 2 characters, at most 8 hits) and scopeFrom.
			Name: "search", Title: "Search by name",
			Description: "Find clusters, services and tasks by name (at least 2 characters; at most 8 hits). Each hit names its kind; use it to find " +
				"the id another tool needs." + scopedNote,
			Route: get("/api/search"),
			Params: joinParams([]Param{
				{Name: "q", Type: TypeString, In: InQuery, Required: true,
					Description: "Part of a cluster, service or task name."},
			}, scopeParams()),
		},
	}
}

// taskFilterParams are the task list's own filters (tasksList in internal/api/tasks.go).
func taskFilterParams() []Param {
	return []Param{
		{Name: "filter", Type: TypeString, In: InQuery, Enum: []string{"problems", "running", "stopped", "all"}, Default: "problems",
			Description: "Which tasks: problems (default), running, stopped or all."},
		{Name: "q", Type: TypeString, In: InQuery,
			Description: "Optional text to match against task id, task definition family and service name."},
	}
}
