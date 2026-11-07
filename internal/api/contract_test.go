package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// contractGolden is read by web/src/test/contract/contract.test.ts, which checks the SPA fixtures against it.
const contractGolden = "../../web/src/test/contract/shapes.json"

// updateContractEnv regenerates the golden instead of failing on a difference.
const updateContractEnv = "HM_UPDATE_CONTRACT"

var (
	timeType    = reflect.TypeOf(time.Time{})
	rawJSONType = reflect.TypeFor[json.RawMessage]()
)

// shapeOf describes how t serialises to JSON: a scalar kind name ("string", "number", "boolean", "time", "any"),
// an object of its json keys, a one-element array for slices, {"$map": elem} for maps, and {"$null": shape} for
// pointers (which may serialise as null); {"$optional": shape} marks an omitempty field that may be absent.
func shapeOf(t reflect.Type) any {
	if t == timeType {
		return "time"
	}
	if t == rawJSONType {
		return "any"
	}
	switch t.Kind() {
	case reflect.Pointer:
		return map[string]any{"$null": shapeOf(t.Elem())}
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Interface:
		return "any"
	case reflect.Slice, reflect.Array:
		return []any{shapeOf(t.Elem())}
	case reflect.Map:
		return map[string]any{"$map": shapeOf(t.Elem())}
	case reflect.Struct:
		fields := map[string]any{}
		collectFields(t, fields)
		return fields
	}
	panic("contract: unsupported type " + t.String())
}

// collectFields adds the JSON keys of struct t, flattening embedded structs like encoding/json does.
func collectFields(t reflect.Type, into map[string]any) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("json")
		name, options, _ := strings.Cut(tag, ",")
		if name == "-" || !field.IsExported() {
			continue
		}
		if field.Anonymous && name == "" {
			collectFields(field.Type, into)
			continue
		}
		if name == "" {
			name = field.Name
		}
		into[name] = shapeOf(field.Type)
		if strings.Contains(","+options+",", ",omitempty,") {
			into[name] = map[string]any{"$optional": into[name]}
		}
	}
}

// nullableField marks a slice field the handler leaves nil (so it serialises as null) when its data source is off.
func nullableField(shape any, key string) any {
	fields := shape.(map[string]any)
	fields[key] = map[string]any{"$null": fields[key]}
	return fields
}

// Known drift source: a nil Go slice serialises as null, but the shapes below say array. Handlers must emit
// empty slices ([]) unless the field is deliberately nullable (see nullableField).
// contractShapes lists every response body the SPA reads, keyed by the name the Vitest contract test uses.
func contractShapes() map[string]any {
	return map[string]any{
		"me":                 shapeOf(reflect.TypeFor[MeDTO]()),
		"login":              shapeOf(reflect.TypeFor[LoginResponse]()),
		"setup":              shapeOf(reflect.TypeFor[SetupStatusDTO]()),
		"overview":           shapeOf(reflect.TypeFor[OverviewDTO]()),
		"clusters":           shapeOf(reflect.TypeFor[ListResponse[ClusterCardDTO]]()),
		"cluster":            shapeOf(reflect.TypeFor[ClusterDTO]()),
		"service":            shapeOf(reflect.TypeFor[ServiceDTO]()),
		"tasks":              shapeOf(reflect.TypeFor[TasksListDTO]()),
		"settings":           shapeOf(reflect.TypeFor[SettingsDTO]()),
		"operations":         shapeOf(reflect.TypeFor[ListResponse[EventDTO]]()),
		"search":             shapeOf(reflect.TypeFor[ItemsResponse[SearchItemDTO]]()),
		"poller":             shapeOf(reflect.TypeFor[ItemsResponse[PollerItemDTO]]()),
		"users":              shapeOf(reflect.TypeFor[ItemsResponse[UserDTO]]()),
		"createUser":         shapeOf(reflect.TypeFor[CreateUserResponse]()),
		"permissions":        shapeOf(reflect.TypeFor[PermissionsResponse]()),
		"tags":               shapeOf(reflect.TypeFor[ItemsResponse[TagItemDTO]]()),
		"tagImpact":          shapeOf(reflect.TypeFor[TagImpactResponse]()),
		"tagAssignments":     shapeOf(reflect.TypeFor[TagAssignmentsResponse]()),
		"accounts":           shapeOf(reflect.TypeFor[AccountsDTO]()),
		"accountTest":        shapeOf(reflect.TypeFor[AccountTestResponse]()),
		"audit":              shapeOf(reflect.TypeFor[ListResponse[AuditEntryDTO]]()),
		"serviceMetrics":     nullableField(shapeOf(reflect.TypeFor[ServiceMetricsDTO]()), "tasks_now"),
		"clusterMetrics":     shapeOf(reflect.TypeFor[ClusterMetricsDTO]()),
		"clustersMetrics":    shapeOf(reflect.TypeFor[ClustersMetricsDTO]()),
		"overviewMetrics":    shapeOf(reflect.TypeFor[OverviewMetricsDTO]()),
		"targetHealth":       shapeOf(reflect.TypeFor[TargetHealthDTO]()),
		"scaling":            shapeOf(reflect.TypeFor[ScalingDTO]()),
		"alerts":             shapeOf(reflect.TypeFor[AlertsDTO]()),
		"logs":               shapeOf(reflect.TypeFor[LogsDTO]()),
		"taskDefinition":     shapeOf(reflect.TypeFor[TaskDefinitionResponse]()),
		"revisions":          shapeOf(reflect.TypeFor[RevisionsResponse]()),
		"revisionDefinition": shapeOf(reflect.TypeFor[RevisionDefinitionResponse]()),
		"imageTags":          shapeOf(reflect.TypeFor[ImageTagsResponse]()),
		"deploySettings":     shapeOf(reflect.TypeFor[DeploySettingsResponse]()),
		"changeset":          shapeOf(reflect.TypeFor[ChangeSetResponse]()),
		"desiredCount":       shapeOf(reflect.TypeFor[DesiredCountResponse]()),
		// redesign v4
		"servicesList":    shapeOf(reflect.TypeFor[ServiceListDTO]()),
		"servicesMetrics": shapeOf(reflect.TypeFor[ServicesMetricsDTO]()),
		// plan 05
		"networks":      shapeOf(reflect.TypeFor[NetworksOverviewDTO]()),
		"networkRegion": shapeOf(reflect.TypeFor[NetworkRegionVPCsDTO]()),
		"vpcTree":       shapeOf(reflect.TypeFor[VPCTreeDTO]()),
		"storage":       shapeOf(reflect.TypeFor[StorageDTO]()),
		// plan 07
		"sso":           shapeOf(reflect.TypeFor[SSOConfigDTO]()),
		"ssoTestResult": shapeOf(reflect.TypeFor[SSOTestResultDTO]()),
		"authOptions":   shapeOf(reflect.TypeFor[AuthOptionsDTO]()),
		// analytics
		"analyticsDelivery":    shapeOf(reflect.TypeFor[DeliveryDTO]()),
		"analyticsReliability": shapeOf(reflect.TypeFor[ReliabilityDTO]()),
		"analyticsCapacity":    shapeOf(reflect.TypeFor[CapacityDTO]()),
		"analyticsOperations":  shapeOf(reflect.TypeFor[OperationsDTO]()),
	}
}

// TestContract_ShapesMatchGolden fails when a DTO changes without the golden (and so the SPA fixtures) being updated.
// Regenerate with HM_UPDATE_CONTRACT=1 go test ./internal/api -run TestContract, then update web/src/test/fixtures.ts.
func TestContract_ShapesMatchGolden(t *testing.T) {
	want, err := json.MarshalIndent(contractShapes(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')

	if os.Getenv(updateContractEnv) == "1" {
		if err := os.MkdirAll(filepath.Dir(contractGolden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(contractGolden, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(contractGolden)
	if err != nil {
		t.Fatalf("read golden (create it with %s=1): %v", updateContractEnv, err)
	}
	if string(got) != string(want) {
		t.Fatalf("API response shapes changed; run %s=1 go test ./internal/api -run TestContract and update the SPA types and fixtures to match", updateContractEnv)
	}
}
