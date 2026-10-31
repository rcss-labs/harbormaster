// Package deploy turns described ECS task definitions into register-ready form and back.
package deploy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// Definition is register-ready task definition JSON with camelCase keys as the AWS CLI shows them.
type Definition = map[string]any

// readOnlyFields are returned by DescribeTaskDefinition but rejected by RegisterTaskDefinition (spec §7.1).
var readOnlyFields = map[string]bool{
	"TaskDefinitionArn":  true,
	"Revision":           true,
	"Status":             true,
	"RequiresAttributes": true,
	"Compatibilities":    true,
	"RegisteredAt":       true,
	"RegisteredBy":       true,
	"DeregisteredAt":     true,
}

// keyOverrides covers SDK field names whose AWS CLI spelling is not a plain lower-cased first letter.
var keyOverrides = map[string]string{
	"FSxWindowsFileServerVolumeConfiguration": "fsxWindowsFileServerVolumeConfiguration",
}

// ToRegisterReady converts a described task definition into register-ready JSON.
//
// It walks the SDK struct by reflection rather than post-processing marshalled JSON, so only
// struct field names are lower-camelled; keys of map-valued fields (logConfiguration.options,
// dockerLabels, firelens options, ...) are copied verbatim by construction.
func ToRegisterReady(td ecstypes.TaskDefinition) (Definition, error) {
	converted, ok := convertValue(reflect.ValueOf(td), true)
	if !ok {
		return nil, fmt.Errorf("task definition is empty")
	}
	def, ok := converted.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected conversion result %T", converted)
	}
	return def, nil
}

// convertValue returns the JSON-ready form of v and false when v is unset and must be dropped.
// Unset means: nil pointer, or a non-pointer zero scalar (SDK optional fields are pointers,
// so a pointer to false/0 is an explicit value and is kept), or an empty collection.
func convertValue(v reflect.Value, topLevel bool) (any, bool) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil, false
		}
		inner := v.Elem()
		if inner.Kind() != reflect.Struct && inner.Kind() != reflect.Slice && inner.Kind() != reflect.Map {
			// An explicit scalar behind a pointer is kept even when it is false or zero.
			return scalar(inner), true
		}
		return convertValue(inner, false)
	case reflect.Struct:
		out := map[string]any{}
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if !field.IsExported() || (topLevel && readOnlyFields[field.Name]) {
				continue
			}
			if value, ok := convertValue(v.Field(i), false); ok {
				out[lowerCamel(field.Name)] = value
			}
		}
		return out, len(out) > 0
	case reflect.Slice:
		items := make([]any, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			if item, ok := convertValue(v.Index(i), false); ok {
				items = append(items, item)
			} else if v.Index(i).Kind() == reflect.Struct || v.Index(i).Kind() == reflect.String {
				// keep empty list entries such as "" so positions are not silently shifted
				items = append(items, scalarOrEmpty(v.Index(i)))
			}
		}
		return items, len(items) > 0
	case reflect.Map:
		out := map[string]any{}
		for _, key := range v.MapKeys() {
			// Map keys are user data: never altered.
			if value, ok := convertValue(v.MapIndex(key), false); ok {
				out[key.String()] = value
			} else {
				out[key.String()] = scalarOrEmpty(v.MapIndex(key))
			}
		}
		return out, len(out) > 0
	case reflect.String:
		return v.String(), v.String() != ""
	case reflect.Bool:
		return v.Bool(), v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), v.Int() != 0
	case reflect.Float32, reflect.Float64:
		return v.Float(), v.Float() != 0
	default:
		return nil, false
	}
}

func scalar(v reflect.Value) any {
	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Bool:
		return v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int()
	case reflect.Float32, reflect.Float64:
		return v.Float()
	}
	return nil
}

func scalarOrEmpty(v reflect.Value) any {
	if v.Kind() == reflect.Struct {
		return map[string]any{}
	}
	return scalar(v)
}

func lowerCamel(name string) string {
	if override, ok := keyOverrides[name]; ok {
		return override
	}
	runes := []rune(name)
	runes[0] = unicode.ToLower(runes[0])
	return string(runes)
}

// ToRegisterInput builds the RegisterTaskDefinition request from a register-ready definition.
// SDK structs have no json tags and encoding/json matches field names case-insensitively,
// so a marshal/unmarshal round trip is enough.
func ToRegisterInput(def Definition, tags []ecstypes.Tag) (*ecs.RegisterTaskDefinitionInput, error) {
	raw, err := json.Marshal(def)
	if err != nil {
		return nil, fmt.Errorf("marshal definition: %w", err)
	}
	var input ecs.RegisterTaskDefinitionInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("decode definition: %w", err)
	}
	input.Tags = tags
	return &input, nil
}

// MainContainer returns the container named serviceName, else the first essential container
// (a missing "essential" counts as essential, the ECS default), else the first container.
// It returns "", -1 when there are no containers. Same rule as model.TaskDefinitionRef.MainContainer.
func MainContainer(def Definition, serviceName string) (name string, index int) {
	containers, _ := def["containerDefinitions"].([]any)
	names := make([]string, len(containers))
	for i, entry := range containers {
		container, _ := entry.(map[string]any)
		names[i], _ = container["name"].(string)
	}
	if len(containers) == 0 {
		return "", -1
	}
	for i, candidate := range names {
		if candidate == serviceName {
			return candidate, i
		}
	}
	for i, entry := range containers {
		container, _ := entry.(map[string]any)
		if essential, set := container["essential"].(bool); !set || essential {
			return names[i], i
		}
	}
	return names[0], 0
}

// ImageTag returns the text after the last ':' of the container's image, "" when there is none.
// A ':' that belongs to a registry host port (before the last '/') does not start a tag.
func ImageTag(def Definition, container string) string {
	containers, _ := def["containerDefinitions"].([]any)
	for _, entry := range containers {
		candidate, _ := entry.(map[string]any)
		if candidate["name"] != container {
			continue
		}
		image, _ := candidate["image"].(string)
		colon := strings.LastIndex(image, ":")
		if colon < 0 || colon < strings.LastIndex(image, "/") {
			return ""
		}
		return image[colon+1:]
	}
	return ""
}

// ParseRevisionARN splits a task definition revision ARN into family and revision.
func ParseRevisionARN(arn string) (family string, revision int, err error) {
	_, resource, found := strings.Cut(arn, ":task-definition/")
	if !found {
		return "", 0, fmt.Errorf("not a task definition ARN: %q", arn)
	}
	family, revisionText, found := strings.Cut(resource, ":")
	if !found || family == "" {
		return "", 0, fmt.Errorf("task definition ARN has no revision: %q", arn)
	}
	revision, err = strconv.Atoi(revisionText)
	if err != nil || revision < 1 {
		return "", 0, fmt.Errorf("invalid revision in %q", arn)
	}
	return family, revision, nil
}

var leadingKeys = []string{"family", "taskRoleArn", "executionRoleArn", "networkMode",
	"requiresCompatibilities", "cpu", "memory", "containerDefinitions", "volumes"}

// DefinitionJSON renders def as 2-space indented JSON with the leading keys in a fixed order
// and the remaining keys alphabetical, so equal definitions always produce equal text.
func DefinitionJSON(def Definition) ([]byte, error) {
	ordered := make([]string, 0, len(def))
	leading := map[string]bool{}
	for _, key := range leadingKeys {
		if _, ok := def[key]; ok {
			ordered = append(ordered, key)
			leading[key] = true
		}
	}
	var rest []string
	for key := range def {
		if !leading[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	ordered = append(ordered, rest...)

	if len(ordered) == 0 {
		return []byte("{}"), nil
	}
	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, key := range ordered {
		name, err := marshalPlain(key, "", "")
		if err != nil {
			return nil, err
		}
		value, err := marshalPlain(def[key], "  ", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal %s: %w", key, err)
		}
		buf.WriteString("  " + name + ": " + value)
		if i < len(ordered)-1 {
			buf.WriteString(",")
		}
		buf.WriteString("\n")
	}
	buf.WriteString("}")
	return buf.Bytes(), nil
}

// marshalPlain indents without HTML escaping, so commands containing & < > stay readable.
func marshalPlain(value any, prefix, indent string) (string, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent(prefix, indent)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}
