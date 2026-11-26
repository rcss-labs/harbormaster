package hmtools

// insightTools are the analytics tabs, networks, storage and config search.
func insightTools() []Tool {
	return []Tool{
		analyticsTool("get_cost_analytics", "Cost analytics", "/api/analytics/cost",
			"Estimated ECS compute cost at list prices over a time range: totals by cluster, account and tag, and saving "+
				"opportunities from rightsizing, Fargate Spot and ARM64."),
		analyticsTool("get_capacity_analytics", "Capacity analytics", "/api/analytics/capacity",
			"Capacity of the estate: reserved vCPU and memory by account and cluster, tasks by capacity provider, services at their "+
				"scaling maximum, subnets low on addresses and over-reserved services. range is accepted, but this view shows the latest "+
				"poll and recent history whatever it is."),
		analyticsTool("get_reliability_analytics", "Reliability analytics", "/api/analytics/reliability",
			"Service availability and task stops over a time range: availability, services below desired count, stops by reason, "+
				"exit codes, the least available services and those getting worse."),
		analyticsTool("get_delivery_analytics", "Delivery analytics", "/api/analytics/delivery",
			"Deployment activity over a time range: rollouts per day, change failure rate, rollout durations, rollbacks, time to "+
				"recover, failure reasons and recent failures, per service."),
		analyticsTool("get_operations_analytics", "Operations analytics", "/api/analytics/operations",
			"Operations made through Harbormaster over a time range: how many by type (deploys, revisions registered, scale "+
				"changes, restarts, stopped tasks, terminal sessions), who made them and the most restarted services."),
		{
			// internal/api/networks.go (listNetworks): scopeFrom.
			Name: "list_networks", Title: "List networks",
			Description: "Per account and region, how many VPCs are in use, clusters and tasks, and tasks not placed in a known VPC. " +
				"Pass an account and region to list_region_vpcs." + scopedNote,
			Route:  get("/api/networks"),
			Params: scopeParams(),
		},
		{
			// internal/api/networks.go (listRegionVPCs): account and region path values, scopeFrom.
			Name: "list_region_vpcs", Title: "List VPCs in a region",
			Description: "The VPCs of one account and region that run visible tasks, with CIDR, subnet count, clusters and task count. " +
				"Pass a VPC id, with the same account and region, to get_vpc_tree." + scopedNote,
			Route: get("/api/networks/{account}/{region}"),
			Params: []Param{
				{Name: "account", Type: TypeString, In: InPath, Required: true,
					Description: "The AWS account id, from list_networks."},
				{Name: "region", Type: TypeString, In: InPath, Required: true,
					Description: "The AWS region, from list_networks."},
			},
		},
		{
			// internal/api/networks.go (getVPCTree): the vpcId path value; account and region are required query values.
			Name: "get_vpc_tree", Title: "Get VPC tree",
			Description: "One VPC as a tree: subnets, the clusters, services and tasks placed in each, load balancers and security group " +
				"edges, with a summary." + scopedNote,
			Route: get("/api/vpcs/{vpcId}/tree"),
			Params: []Param{
				{Name: "vpc_id", Type: TypeString, In: InPath, Required: true,
					Description: "The VPC id (vpc-...), from list_region_vpcs."},
				{Name: "account", Type: TypeString, In: InQuery, Required: true,
					Description: "The VPC's AWS account id."},
				{Name: "region", Type: TypeString, In: InQuery, Required: true,
					Description: "The VPC's AWS region."},
			},
		},
		{
			// internal/api/storage.go (listStorage): scopeFrom.
			Name: "list_storage", Title: "List storage",
			Description: "The EFS file systems in the estate: size, throughput and performance mode, client connections, whether they are " +
				"mounted, and which services mount each, with access points and paths." +
				scopedNote,
			Route:  get("/api/storage"),
			Params: scopeParams(),
		},
		{
			// internal/api/config_search.go (getConfigSearch): q and scopeFrom.
			Name: "search_config", Title: "Search service configuration",
			Description: "Find services by what their current task definitions contain: image, tag, base image, environment variable or secret " +
				"names, ports, roles and log groups. Matches names only, never environment values or secret references. Terms are words " +
				"(all must match), optionally prefixed with a field (image:, tag:, base:, env:, secret:, port:, role:, taskrole:, execrole:, " +
				"loggroup:), quoted for an exact value or prefixed with - to negate." + scopedNote,
			Route: get("/api/config-search"),
			Params: joinParams([]Param{
				{Name: "q", Type: TypeString, In: InQuery, Required: true,
					Description: "The search terms, for example: image:nginx env:DATABASE_URL -tag:latest"},
			}, scopeParams()),
		},
	}
}

// analyticsTool declares one analytics tab: scopeFrom plus analyticsRangeFrom
// (internal/api/analytics.go: 24h, 7d, 30d or 90d; default 30d).
func analyticsTool(name, title, pattern, summary string) Tool {
	return Tool{
		Name: name, Title: title,
		Description: summary + " The range may be shortened to what Harbormaster still keeps; the answer says which range was used." + scopedNote,
		Route:       get(pattern),
		Params: joinParams([]Param{
			{Name: "range", Type: TypeString, In: InQuery, Enum: []string{"24h", "7d", "30d", "90d"}, Default: "30d",
				Description: "Time window: 24h, 7d, 30d (default) or 90d."},
		}, scopeParams()),
	}
}
