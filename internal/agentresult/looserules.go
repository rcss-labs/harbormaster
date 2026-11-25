package agentresult

import "strings"

// looseRule says what is loose in a JSON value. The nil rule is of a value no rule names: nothing in it is loose.
type looseRule struct {
	leaf    bool                  // a string here is loose
	items   *looseRule            // of an array's elements
	members map[string]*looseRule // of an object's members; "*" is any other
}

func (r *looseRule) text() bool { return r != nil && r.leaf }

func (r *looseRule) item() *looseRule {
	if r == nil {
		return nil
	}
	return r.items
}

func (r *looseRule) member(name string) *looseRule {
	if r == nil {
		return nil
	}
	if m, ok := r.members[name]; ok {
		return m
	}
	return r.members["*"]
}

// Pieces that more than one rule uses.
const (
	ruleTag      = `tag{name color} `
	ruleCluster  = `cluster{id name} `
	ruleService  = `service{id name status} `
	ruleEvent    = `actor source text ts ` + ruleCluster + ruleService
	ruleTaskItem = `account_name arn az capacity cpu family group health id memory private_ip region short_id started_at started_by status ` +
		`stop_cause stop_code stop_kind stop_reason_group stopped_at stopped_reason subnet_id ` + ruleCluster + ruleService
	ruleTasks = `items[]{` + ruleTaskItem + `} next polled_at summary{by_az[]{az} by_cluster[]{id name} stop_reasons[]{reason}}`

	ruleTimelineEvent = `id lane severity title actor detail op task_id at evidence_at diff[]{label from to} `

	ruleAnalytics = `tab polled_at window{bucket from to requested retention used} sources[]{kept reads setting table window_used} `
	ruleSeries    = `{series[]{key label} timestamps[]} `
	ruleRollup    = `{data_since data` + ruleSeries + `} `
	ruleImageARM  = `images[]{account container image reason state} `

	// Error texts of a scan (base_image.message, failure_detail, and failure_action, which quotes it) are not named.
	ruleImage = `digest image repository tag registry_account_id registry_region status failure_code platform scanned_at ` +
		`matched_db_built_at syft_version last_requested_at base_image{ref scanned_ref state} ` +
		`requests[]{service_id service cluster container tag source by at permission_tag{name color}} `

	// Environment values, commands, entry points and health checks are not named, and neither is what a container
	// may carry under names of its own: labels, log driver options beyond the awslogs ones, driver and system options.
	ruleDefinition = `definition{family taskRoleArn executionRoleArn networkMode pidMode ipcMode cpu memory requiresCompatibilities[] ` +
		`runtimePlatform{cpuArchitecture operatingSystemFamily} placementConstraints[]{type expression} ` +
		`volumes[]{name host{sourcePath} efsVolumeConfiguration{fileSystemId rootDirectory transitEncryption authorizationConfig{accessPointId iam}}} ` +
		`containerDefinitions[]{name image user workingDirectory hostname versionConsistency environment[]{name} secrets[]{name valueFrom} ` +
		`repositoryCredentials{credentialsParameter} portMappings[]{name protocol appProtocol containerPortRange} ` +
		`dependsOn[]{containerName condition} mountPoints[]{sourceVolume containerPath} volumesFrom[]{sourceContainer} ulimits[]{name} ` +
		`resourceRequirements[]{type value} ` +
		`logConfiguration{logDriver options{awslogs-group awslogs-region awslogs-stream-prefix awslogs-create-group mode max-buffer-size} secretOptions[]{name valueFrom}}}} `

	ruleNode = `kind key label vpc{vpc_id cidr} subnet{subnet_id cidr} cluster{cluster_id ` + ruleTag + `} service{service_id status} ` +
		`task{task_id service_id private_ip group} `
)

// looseRules names, for each tool, the members whose strings are loose: "name" is a string, "name[]" a list of them,
// "name{...}" an object, "name[]{...}" a list of objects. Everything else of an answer is strict, so a tool or member
// that is added later is strict until it is named here. Error texts, log messages, environment values and command text
// are never named. Each entry mirrors the DTO of the tool's route; a test in internal/api holds the two together.
var looseRules = map[string]string{
	"whoami":       `user{username display_name} token{public_id name kind scope expires_at} tags[]{name} log_search{resets_at}`,
	"get_overview": `polled_at clusters[]{id name status} per_cluster[]{id name} tasks{history_72h[]{t}} stops_per_hour[]{hour} deployments_per_day[]{day} stop_reasons[]{reason} events[]{` + ruleEvent + `}`,
	"list_clusters": `next polled_at items[]{account_id account_name id name region status last_change_at ` + ruleTag +
		`services[]{id name status}}`,
	"get_cluster": `account_id account_name arn console_url id name region status last_change_at polled_at ` + ruleTag +
		`services[]{id name status} stops_per_hour[]{hour} tasks_history_72h[]{t} vpc{vpc_id name} ` +
		`services_detail[]{cpu family id image_tag last_change_at memory name status slots[]{health private_ip short_id task_id} ` + ruleTag + `}`,
	"list_services": `polled_at items[]{account_id account_name attention cpu family id image_tag last_change_at memory name region rollout status ` +
		ruleTag + ruleCluster + `}`,
	"get_service": `account_id account_name console_url deployment_controller family id image_tag launch_type name platform_version polled_at region ` +
		`status strategy_text task_definition_arn ` + ruleTag + ruleCluster + `controller{banner name type} counts_history_72h[]{t} ` +
		`deployments[]{id status rollout_state rollout_state_reason created_at updated_at} ` +
		`deployments_14d[]{deployment_id kind outcome started_at finished_at} ` +
		`networking{load_balancers[]{container_name target_group_name} security_group_names{*} security_groups[] subnets[]{id} vpc{vpc_id name}} ` +
		`recent_events[]{` + ruleEvent + `} running_tasks[]{az health id private_ip short_id subnet_id} slots[]{health private_ip short_id task_id} ` +
		`task_definition{cpu family memory task_role_arn containers[]{name image} efs_mounts[]{access_point_id container_path file_system_id}}`,
	"list_service_tasks": ruleTasks,
	"list_tasks":         ruleTasks,
	"search":             `items[]{href kind label sub}`,

	"get_task_definition": `family main_container polled_at strategy task_definition_arn controller{name type} tags[]{key value} ` + ruleTag + ruleDefinition,
	"list_revisions":      `items[]{arn cpu image_tag memory registered_at registered_by status}`,
	"get_revision":        `arn registered_at registered_by ` + ruleDefinition,
	"get_scaling": `polled_at unreadable_parts[] policies[]{metric name type} scheduled_actions[]{name schedule timezone} ` +
		`activities[]{cause description id started_at ended_at status_code status_message}`,
	"get_target_health": `polled_at groups[]{name target_group_arn targets[]{az description id reason state}}`,
	"get_service_timeline": `range from to polled_at events[]{` + ruleTimelineEvent + `} lanes[]{lane note} ` +
		`incident{first_failure_at last_failure_at top_reason trigger{` + ruleTimelineEvent + `} rolled_back{` + ruleTimelineEvent + `}}`,
	"list_service_operations": `next polled_at items[]{` + ruleEvent + `}`,
	"get_service_metrics": `insights polled_at range timestamps[] alb{err5xx_per_hour{timestamps[]}} markers[]{at kind label service} ` +
		`stale{fetched_at} tasks_now[]{task_id}`,
	"get_service_offhours": `reason schedule_name state timezone until`,
	"get_desired_count":    ``,
	"get_arm64_readiness":  `status checked_at ` + ruleImageARM,
	"get_timeline_task":    ruleTaskItem,

	"list_alerts": `polled_at items[]{at cause cluster cluster_id kind service service_id severity tab text title ` +
		`just_before{at source text} next_step{href label}}`,
	"get_service_logs":         `group region older_before containers[]{name driver reason stream_prefix} lines[]{t container task_id}`,
	"list_log_search_services": `next polled_at items[]{account_id account_name cluster id name region}`,
	"estimate_log_search":      ``,
	"search_logs": `from to rows[]{t service_id container task_id} services[]{id name state} ` +
		`targets[]{account_id account_name region state}`,

	"list_security":        `hint note polled_at db{built_at freshness} scanner{failing_since last_success_at} images[]{` + ruleImage + `}`,
	"get_service_security": `images[]{` + ruleImage + `}`,
	"get_image_security": `hint ` + ruleImage + `findings[]{vuln_id severity package version type location fixed_version fix_state data_source url ` +
		`first_seen_at origin acceptances[]{service_id service by reason created_at expires_at}}`,

	"get_cost_analytics": ruleAnalytics + `prices_as_of spot_prices_as_of by_account[]{key label} by_cluster[]{key label} by_tag[]{key label} ` +
		`kpis{unpriced_regions[]} arm64{services[]{cluster region service service_id status ` + ruleTag + ruleImageARM + `}} ` +
		`rightsizing{state services[]{architecture cluster region service service_id status ` + ruleTag + `}} ` +
		`spot{services[]{cluster controller_name region service service_id status stop_evidence ` + ruleTag + `}}`,
	"get_capacity_analytics": ruleAnalytics + `at_scaling_max[]{at_max_since cluster service service_id} ` +
		`clusters{rows[]{account_id account_name cluster cluster_id ` + ruleTag + `}} ` +
		`over_reserved{data_since data{rows[]{cluster service service_id ` + ruleTag + `}}} subnets_low[]{account_id az name region subnet_id} ` +
		`tasks_by_capacity_provider{slices[]{key label}} vcpu_by_account{slices[]{key label}} reserved_vcpu_7d` + ruleSeries +
		`memory_90d` + ruleRollup + `reserved_vs_used_90d` + ruleRollup + `spot_share_90d` + ruleRollup,
	"get_reliability_analytics": ruleAnalytics + `least_available{rows[]{cluster service service_id top_reason ` + ruleTag + `}} ` +
		`getting_worse{data_since data{rows[]{cluster service service_id ` + ruleTag + `}}} availability` + ruleSeries + `exit_codes` + ruleSeries +
		`services_below` + ruleSeries + `stops_by_reason` + ruleSeries + `unexpected_by_zone` + ruleSeries +
		`availability_90d` + ruleRollup + `below_desired_90d` + ruleRollup,
	"get_delivery_analytics": ruleAnalytics + `duration` + ruleSeries + `failure_rate` + ruleSeries + `rollouts` + ruleSeries + `failure_reasons` + ruleRollup +
		`recent_failures{data_since data{rows[]{at cluster detail reason service service_id}}} ` +
		`services{rows[]{account_id cluster last_failed_at service service_id ` + ruleTag + `}}`,
	"get_operations_analytics": ruleAnalytics + `by_type` + ruleSeries + `hands_on` + ruleSeries + `restarts_per_100_deploys` + ruleSeries +
		`most_restarted{rows[]{cluster last_at service service_id ` + ruleTag + `}} who{rows[]{last_at username}}`,

	"list_networks":    `polled_at items[]{account_id account_name regions[]{region}}`,
	"list_region_vpcs": `polled_at items[]{cidr clusters[] name vpc_id}`,
	// A tree node holds nodes: vpc, zone, subnet, cluster, service, task.
	"get_vpc_tree": `polled_at vpc{vpc_id name cidr account_id account_name region} edges[]{kind id subnet_id detail} ` +
		`root{` + ruleNode + strings.Repeat(`children[]{`+ruleNode, 5) + strings.Repeat(`}`, 6),
	"list_storage": `polled_at items[]{account_id file_system_id name performance_mode region throughput_mode vpc{vpc_id name} size_30d[]{t} throughput_24h[]{t} ` +
		`access_points[]{access_point_id path} mounted_by[]{cluster container_path service service_id}}`,
	"search_config": `query polled_at items[]{account_id account_name execution_role family id name region task_role ` + ruleTag + ruleCluster +
		`matches[]{container field value} containers[]{base_image image log_group name tag env_names[] secret_names[]}}`,
}

var looseText = func() map[string]*looseRule {
	parsed := make(map[string]*looseRule, len(looseRules))
	for tool, source := range looseRules {
		members, _ := parseLoose(source)
		parsed[tool] = &looseRule{members: members}
	}
	return parsed
}()

// parseLoose reads members up to the closing brace or the end, and returns what follows that brace.
func parseLoose(source string) (members map[string]*looseRule, rest string) {
	members = map[string]*looseRule{}
	for {
		source = strings.TrimLeft(source, " ")
		if source == "" {
			return members, ""
		}
		if source[0] == '}' {
			return members, source[1:]
		}
		end := strings.IndexAny(source, " {}[")
		if end < 0 {
			end = len(source)
		}
		rule := &looseRule{}
		members[source[:end]] = rule
		source = source[end:]
		if after, isList := strings.CutPrefix(source, "[]"); isList {
			rule.items = &looseRule{}
			rule, source = rule.items, after
		}
		if after, isObject := strings.CutPrefix(source, "{"); isObject {
			rule.members, source = parseLoose(after)
		} else {
			rule.leaf = true
		}
	}
}
