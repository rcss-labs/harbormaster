package api

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/hmtools"
	"github.com/rcss-labs/harbormaster/internal/network"
)

// toolAnswer is the body of one agent tool's route. strict lists the members the AI summary check holds to the short
// run: log messages, environment values, commands and error texts. Every other string of the body is of the loose class.
// extra lists members of raw JSON, which a Go type does not describe.
type toolAnswer struct {
	body          reflect.Type
	strict, extra []string
}

var (
	definitionLoose = []string{
		"definition.family", "definition.executionRoleArn", "definition.taskRoleArn", "definition.networkMode", "definition.cpu",
		"definition.requiresCompatibilities[]", "definition.runtimePlatform.cpuArchitecture", "definition.volumes[].name",
		"definition.volumes[].efsVolumeConfiguration.authorizationConfig.accessPointId",
		"definition.containerDefinitions[].name", "definition.containerDefinitions[].image",
		"definition.containerDefinitions[].environment[].name", "definition.containerDefinitions[].secrets[].valueFrom",
		"definition.containerDefinitions[].portMappings[].protocol", "definition.containerDefinitions[].dependsOn[].condition",
		"definition.containerDefinitions[].logConfiguration.options.awslogs-group",
		"definition.containerDefinitions[].logConfiguration.secretOptions[].valueFrom",
	}
	definitionStrict = []string{
		"definition.containerDefinitions[].environment[].value", "definition.containerDefinitions[].command[]",
		"definition.containerDefinitions[].entryPoint[]", "definition.containerDefinitions[].healthCheck.command[]",
		"definition.containerDefinitions[].environmentFiles[].value", "definition.containerDefinitions[].dockerLabels.label",
		"definition.containerDefinitions[].logConfiguration.options.vendor-option", "definition.containerDefinitions[].extraHosts[].hostname",
		"definition.containerDefinitions[].systemControls[].value", "definition.containerDefinitions[].firelensConfiguration.options.x",
		"definition.proxyConfiguration.properties[].value", "definition.volumes[].dockerVolumeConfiguration.driverOpts.x",
		"definition.containerDefinitions[]", "definition.containerDefinitions", "definition", "definition.invented",
	}
	nodeLoose = []string{
		"root.kind", "root.label", "root.children[].children[].children[].children[].children[].task.task_id",
		"root.children[].children[].children[].children[].service.service_id", "root.children[].children[].subnet.cidr",
	}
	scanErrors = []string{"images[].base_image.message", "images[].failure_detail", "images[].failure_action"}
)

func toolAnswers() map[string]toolAnswer {
	of := func(body reflect.Type, strict ...string) toolAnswer { return toolAnswer{body: body, strict: strict} }
	return map[string]toolAnswer{
		"whoami":                    of(reflect.TypeFor[WhoAmIDTO]()),
		"get_overview":              of(reflect.TypeFor[OverviewDTO]()),
		"list_clusters":             of(reflect.TypeFor[ListResponse[ClusterCardDTO]]()),
		"get_cluster":               of(reflect.TypeFor[ClusterDTO]()),
		"list_services":             of(reflect.TypeFor[ServiceListDTO]()),
		"get_service":               of(reflect.TypeFor[ServiceDTO]()),
		"list_service_tasks":        of(reflect.TypeFor[TasksListDTO]()),
		"list_tasks":                of(reflect.TypeFor[TasksListDTO]()),
		"search":                    of(reflect.TypeFor[ItemsResponse[SearchItemDTO]]()),
		"get_task_definition":       {reflect.TypeFor[TaskDefinitionResponse](), definitionStrict, definitionLoose},
		"list_revisions":            of(reflect.TypeFor[RevisionsResponse]()),
		"get_revision":              {reflect.TypeFor[RevisionDefinitionResponse](), definitionStrict, definitionLoose},
		"get_scaling":               of(reflect.TypeFor[ScalingDTO]()),
		"get_target_health":         of(reflect.TypeFor[TargetHealthDTO]()),
		"get_service_timeline":      of(reflect.TypeFor[TimelineDTO]()),
		"list_service_operations":   of(reflect.TypeFor[ListResponse[EventDTO]]()),
		"get_service_metrics":       of(reflect.TypeFor[ServiceMetricsDTO](), "stale.reason"),
		"get_service_offhours":      of(reflect.TypeFor[ServiceOffHoursDTO]()),
		"get_desired_count":         of(reflect.TypeFor[DesiredCountResponse]()),
		"get_arm64_readiness":       of(reflect.TypeFor[ARM64ReadinessDTO]()),
		"get_timeline_task":         of(reflect.TypeFor[TaskDTO]()),
		"list_alerts":               of(reflect.TypeFor[AlertsDTO](), "items[].investigation", "items[].investigation.reason"),
		"get_service_logs":          of(reflect.TypeFor[LogsDTO](), "lines[].message"),
		"list_log_search_services":  of(reflect.TypeFor[ListResponse[LogSearchServiceDTO]]()),
		"estimate_log_search":       of(reflect.TypeFor[LogSearchEstimateDTO]()),
		"search_logs":               of(reflect.TypeFor[LogSearchResultDTO](), "rows[].message", "services[].message", "targets[].message"),
		"list_security":             of(reflect.TypeFor[SecurityDTO](), append([]string{"db.last_refresh_error", "scanner.last_error"}, scanErrors...)...),
		"get_service_security":      of(reflect.TypeFor[SecurityServiceDTO](), scanErrors...),
		"get_image_security":        of(reflect.TypeFor[SecurityImageDetailDTO](), "base_image.message", "failure_detail", "failure_action"),
		"get_cost_analytics":        of(reflect.TypeFor[CostDTO]()),
		"get_capacity_analytics":    of(reflect.TypeFor[CapacityDTO]()),
		"get_reliability_analytics": of(reflect.TypeFor[ReliabilityDTO]()),
		"get_delivery_analytics":    of(reflect.TypeFor[DeliveryDTO]()),
		"get_operations_analytics":  of(reflect.TypeFor[OperationsDTO]()),
		"list_networks":             of(reflect.TypeFor[NetworksOverviewDTO]()),
		"list_region_vpcs":          of(reflect.TypeFor[NetworkRegionVPCsDTO]()),
		"get_vpc_tree":              {reflect.TypeFor[VPCTreeDTO](), []string{"root", "root.invented"}, nodeLoose},
		"list_storage":              of(reflect.TypeFor[StorageDTO](), "metrics_error"),
		"search_config":             of(reflect.TypeFor[ConfigSearchResponse]()),
	}
}

// stringPaths lists the members of a shape (see shapeOf) whose JSON value is a string. Raw JSON is not looked into.
func stringPaths(shape any, path string) (paths []string) {
	switch s := shape.(type) {
	case string:
		if s == "string" || s == "time" {
			return []string{path}
		}
	case []any:
		return stringPaths(s[0], path+"[]")
	case map[string]any:
		for name, member := range s {
			switch name {
			case "$null", "$optional":
				paths = append(paths, stringPaths(member, path)...)
			case "$map":
				paths = append(paths, stringPaths(member, path+".any-key")...)
			default:
				paths = append(paths, stringPaths(member, strings.TrimPrefix(path+"."+name, "."))...)
			}
		}
	}
	return paths
}

// answerWith is an answer that holds text at path and nothing else.
func answerWith(path, text string) any {
	if path == "" {
		return text
	}
	first, rest, _ := strings.Cut(path, ".")
	name, lists := strings.TrimSuffix(first, "[]"), strings.Count(first, "[]")
	value := answerWith(rest, text)
	if lists == 1 {
		value = []any{value}
	}
	return map[string]any{name: value}
}

// A tool or a string that a later change adds has no row here and no rule: the test fails until somebody decides its
// class. Until then it is strict.
func TestEveryStringOfAToolAnswerHasADecidedClass(t *testing.T) {
	const text = "a run of words that is longer than the short limit of copies" // between the two runs
	isStrict := func(tool, path string) bool {
		t.Helper()
		body, err := json.Marshal(answerWith(path, text))
		if err != nil {
			t.Fatal(err)
		}
		read := &agentresult.ReadCorpus{}
		read.Add(tool, string(body))
		_, replaced := agentresult.CheckReadSummary(text, read)
		return replaced
	}
	answers := toolAnswers()
	tools := hmtools.Tools()
	for _, tool := range tools {
		answer, ok := answers[tool.Name]
		if !ok {
			t.Errorf("%s has no row", tool.Name)
			continue
		}
		paths := stringPaths(shapeOf(answer.body), "")
		for _, path := range append(paths, answer.extra...) {
			if want := slices.Contains(answer.strict, path); isStrict(tool.Name, path) != want {
				t.Errorf("%s: %s is strict: %v, want %v", tool.Name, path, !want, want)
			}
		}
		for _, path := range append(slices.Clone(answer.strict), "invented", "items[].invented", "invented[].name") {
			if !isStrict(tool.Name, path) {
				t.Errorf("%s: %s is not strict", tool.Name, path)
			}
		}
		if !isStrict("invented_"+tool.Name, "name") {
			t.Errorf("an invented tool's name is not strict")
		}
	}
	if len(answers) != len(tools) {
		t.Errorf("%d rows for %d tools", len(answers), len(tools))
	}
}

// The tree's node type is recursive, so its members are compared by hand with one level.
func TestEveryStringOfATreeNodeHasADecidedClass(t *testing.T) {
	node := reflect.TypeFor[network.Node]()
	for i := range node.NumField() {
		field := node.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "children" {
			continue
		}
		for _, path := range stringPaths(shapeOf(field.Type), "root."+name) {
			const text = "a run of words that is longer than the short limit of copies"
			body, _ := json.Marshal(answerWith(path, text))
			read := &agentresult.ReadCorpus{}
			read.Add("get_vpc_tree", string(body))
			if _, replaced := agentresult.CheckReadSummary(text, read); replaced {
				t.Errorf("%s is strict", path)
			}
		}
	}
}
