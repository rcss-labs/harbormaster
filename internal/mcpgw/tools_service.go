package mcpgw

// serviceTools read one service in depth. Every one takes a service_id;
// account, region and tag filters are not offered because the id already
// fixes the service. Handlers are in internal/api (see each entry).
func serviceTools() []Tool {
	return []Tool{
		{
			// internal/api/change_read.go (getTaskDefinition): the service id path value only.
			Name: "get_task_definition", Title: "Get task definition",
			Description: "The task definition the service currently runs: containers, images, CPU and memory, ports, environment variable names " +
				"and values, secret references (never secret values), log configuration and roles. For older revisions use list_revisions and get_revision.",
			Route:  get("/api/services/{serviceId}/task-definition"),
			Params: []Param{serviceIDParam()},
		},
		{
			// internal/api/change_read.go (getRevisions): the service id path value only.
			Name: "list_revisions", Title: "List task definition revisions",
			Description: "The task definition revisions of the service's family, newest first, with status and registration time. " +
				"Pass a revision number to get_revision.",
			Route:  get("/api/services/{serviceId}/revisions"),
			Params: []Param{serviceIDParam()},
		},
		{
			// internal/api/change_read.go (getRevisionDefinition): serviceId and revision (an integer) path values.
			Name: "get_revision", Title: "Get task definition revision",
			Description: "One revision of the service's task definition family in full, in the same shape as get_task_definition. " +
				"Revision numbers come from list_revisions.",
			Route: get("/api/services/{serviceId}/revisions/{revision}"),
			Params: []Param{
				serviceIDParam(),
				{Name: "revision", Type: TypeInteger, In: InPath, Required: true,
					Description: "The revision number, from list_revisions."},
			},
		},
		{
			// internal/api/observe_scaling.go (getScaling): service id and scopeFrom (filters unused here).
			Name: "get_scaling", Title: "Get auto scaling",
			Description: "The service's Application Auto Scaling setup: whether it has a scalable target, minimum and maximum, policies, " +
				"scheduled actions and recent scaling activities." + scopedNote,
			Route:  get("/api/services/{serviceId}/scaling"),
			Params: []Param{serviceIDParam()},
		},
		{
			// internal/api/observe_metrics.go (getTargetHealth): service id and scopeFrom (filters unused here).
			Name: "get_target_health", Title: "Get load balancer target health",
			Description: "Health of the service's load balancer targets, per target group, with healthy and unhealthy counts and the reason " +
				"for each unhealthy target.",
			Route:  get("/api/services/{serviceId}/target-health"),
			Params: []Param{serviceIDParam()},
		},
		{
			// internal/api/timeline.go (getTimeline): range (1h, 6h, 24h, 7d; default 1h).
			Name: "get_service_timeline", Title: "Get service timeline",
			Description: "What happened to the service over a time range: deployments, operations, task stops with reasons, active alerts and " +
				"the most recent incident. Task ids here can be passed to get_timeline_task. Built from recorded history; holds no log lines.",
			Route: get("/api/services/{serviceId}/timeline"),
			Params: []Param{
				serviceIDParam(),
				{Name: "range", Type: TypeString, In: InQuery, Enum: []string{"1h", "6h", "24h", "7d"}, Default: "1h",
					Description: "How far back to look: 1h (default), 6h, 24h or 7d."},
			},
		},
		{
			// internal/api/operations.go (serviceOperations): include_ecs, limitFrom, a time cursor.
			Name: "list_service_operations", Title: "List service operations",
			Description: "The service's operations log, newest first: changes made through Harbormaster (what, who and when) merged with the " +
				"service's ECS events unless include_ecs is false. Paged: pass next_cursor back as cursor." + scopedNote,
			Route: get("/api/services/{serviceId}/operations"),
			Params: joinParams([]Param{
				serviceIDParam(),
				{Name: "include_ecs", Type: TypeBoolean, In: InQuery, Default: true,
					Description: "Merge in the service's ECS events (default true)."},
			}, pageParams()),
		},
		{
			// internal/api/observe_metrics.go (getServiceMetrics): rangeFrom (1h, 6h, 24h, 72h; default 24h).
			Name: "get_service_metrics", Title: "Get service metrics",
			Description: "CloudWatch metrics for the service over a time range: CPU and memory utilisation as time series, current values per " +
				"task, and load balancer metrics where it has a load balancer.",
			Route: get("/api/services/{serviceId}/metrics"),
			Params: []Param{
				serviceIDParam(),
				{Name: "range", Type: TypeString, In: InQuery, Enum: []string{"1h", "6h", "24h", "72h"}, Default: "24h",
					Description: "How far back: 1h, 6h, 24h (default) or 72h."},
			},
		},
		{
			// internal/api/offhours.go (getServiceOffHours): the service id path value only.
			Name: "get_service_offhours", Title: "Get off-hours state",
			Description: "Whether the service is enrolled in an off-hours schedule (scaled to zero outside working hours), the schedule and its " +
				"timezone, and its state now with until when and why. Read-only: this server cannot wake or sleep a service.",
			Route:  get("/api/services/{serviceId}/offhours"),
			Params: []Param{serviceIDParam()},
		},
		{
			// internal/api/change_read.go (getDesiredCount): the service id path value only.
			Name: "get_desired_count", Title: "Get desired count",
			Description: "The service's desired and running task counts, the most the desired count may be set to, and the auto scaling range " +
				"if it has one. Read-only: this server cannot change it.",
			Route:  get("/api/services/{serviceId}/desired-count"),
			Params: []Param{serviceIDParam()},
		},
		{
			// internal/api/arm64_readiness.go (getARM64Readiness): the service id path value only.
			Name: "get_arm64_readiness", Title: "Get ARM64 readiness",
			Description: "Whether the images of the service's current task definition can run on ARM64 (Graviton): ready, blocked or unknown, " +
				"per container image, and when that was checked.",
			Route:  get("/api/services/{serviceId}/arm64-readiness"),
			Params: []Param{serviceIDParam()},
		},
		{
			// internal/api/timeline.go (getTimelineTask): serviceId and taskId path values.
			Name: "get_timeline_task", Title: "Get a task from the timeline",
			Description: "One task of the service in detail, including stopped tasks older than list_service_tasks shows: status, stop reason, " +
				"containers and exit codes. The task id comes from get_service_timeline or list_service_tasks." + scopedNote,
			Route: get("/api/services/{serviceId}/timeline/tasks/{taskId}"),
			Params: []Param{
				serviceIDParam(),
				{Name: "task_id", Type: TypeString, In: InPath, Required: true,
					Description: "The ECS task id (the last part of the task ARN), from get_service_timeline or list_service_tasks."},
			},
		},
	}
}
