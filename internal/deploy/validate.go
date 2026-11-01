package deploy

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// ValidationError is a user-correctable problem with a task definition draft; the API maps it to 422 "invalid".
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

// Validate checks a draft against the definition it was edited from (spec §7.1).
// It returns nil or a *ValidationError.
func Validate(base, draft Definition, admin bool, accountID string) error {
	_, err := Prepare(base, draft, admin, accountID, nil)
	return err
}

// Prepare validates a draft and returns the request that is safe to send to AWS.
//
// Security: the rules run on the canonical form of the request that will actually be sent
// (the draft after encoding/json decoded it into the SDK struct), never on the submitted map.
// encoding/json matches keys case-insensitively and the last one wins, so a rule that reads
// the map's exact keys can be bypassed with `taskrolearn`. Drafts holding case-folded
// duplicate keys, or keys no request field matches, are rejected up front as well.
// Numeric cpu/memory in the draft are rewritten as decimal strings, the form AWS expects.
func Prepare(base, draft Definition, admin bool, accountID string, tags []ecstypes.Tag) (*ecs.RegisterTaskDefinitionInput, error) {
	if err := checkFoldedKeys(map[string]any(draft), reflect.TypeOf(ecs.RegisterTaskDefinitionInput{}), ""); err != nil {
		return nil, err
	}
	for _, key := range []string{"cpu", "memory"} {
		if err := normalizeSizeField(draft, key); err != nil {
			return nil, err
		}
	}
	input, err := decodeDefinition(draft, tags, true)
	if err != nil {
		return nil, invalidDecode(err)
	}
	canonical, err := canonicalDefinition(input)
	if err != nil {
		return nil, invalid("Task definition JSON is invalid: %s", err)
	}
	if err := validateCanonical(base, canonical, admin, accountID); err != nil {
		return nil, err
	}
	return input, nil
}

// invalidDecode turns a decode failure into the user-facing message, naming unknown keys.
func invalidDecode(err error) error {
	const unknownPrefix = "decode definition: json: unknown field "
	if text := err.Error(); strings.HasPrefix(text, unknownPrefix) {
		return invalid("Task definition JSON is invalid: unknown key %s", strings.TrimPrefix(text, unknownPrefix))
	}
	return invalid("Task definition JSON is invalid: %s", strings.TrimPrefix(err.Error(), "decode definition: json: "))
}

// validateCanonical applies the business rules to a canonical draft.
func validateCanonical(base, draft Definition, admin bool, accountID string) error {
	containers, _ := draft["containerDefinitions"].([]any)
	if len(containers) == 0 {
		return invalid("Task definition JSON is invalid: containerDefinitions is required")
	}
	for i, entry := range containers {
		container, _ := entry.(map[string]any)
		name, _ := container["name"].(string)
		image, _ := container["image"].(string)
		if name == "" || image == "" {
			return invalid("Task definition JSON is invalid: containerDefinitions[%d] needs a name and an image", i)
		}
	}

	if draft["family"] != base["family"] {
		return invalid("Use Terraform to create a new family")
	}

	if !hasEssentialContainer(containers) {
		return invalid("At least one container must be essential")
	}

	if err := validateFargateSize(draft); err != nil {
		return err
	}

	if admin {
		return nil
	}
	if draft["taskRoleArn"] != base["taskRoleArn"] || draft["executionRoleArn"] != base["executionRoleArn"] {
		return invalid("Changing roles needs a global admin")
	}
	if err := validateExecutionFetches(base, draft); err != nil {
		return err
	}
	return validateSecrets(base, draft, accountID)
}

// hasEssentialContainer treats a missing "essential" as true, the ECS default.
func hasEssentialContainer(containers []any) bool {
	for _, entry := range containers {
		container, _ := entry.(map[string]any)
		if essential, set := container["essential"].(bool); !set || essential {
			return true
		}
	}
	return false
}

// normalizeSizeField rewrites a numeric cpu/memory value as its decimal string.
// A non-integral number is rejected rather than truncated.
func normalizeSizeField(def Definition, key string) error {
	switch value := def[key].(type) {
	case float64:
		if value != math.Trunc(value) || math.IsInf(value, 0) {
			return invalid("Task definition JSON is invalid: %s must be a whole number", key)
		}
		def[key] = strconv.FormatInt(int64(value), 10)
	case int:
		def[key] = strconv.Itoa(value)
	case int64:
		def[key] = strconv.FormatInt(value, 10)
	case json.Number:
		def[key] = value.String()
	}
	return nil
}

// validateFargateSize applies only when FARGATE is requested and both task-level sizes are set
// as plain integers; ECS itself rejects a missing size or a vCPU/GB spelling.
func validateFargateSize(draft Definition) error {
	compatibilities, _ := draft["requiresCompatibilities"].([]any)
	fargate := false
	for _, entry := range compatibilities {
		if entry == "FARGATE" {
			fargate = true
		}
	}
	if !fargate {
		return nil
	}
	cpuText, cpuSet := draft["cpu"].(string)
	memoryText, memorySet := draft["memory"].(string)
	if !cpuSet || !memorySet {
		return nil
	}
	cpu, cpuErr := strconv.Atoi(cpuText)
	memory, memoryErr := strconv.Atoi(memoryText)
	if cpuErr != nil || memoryErr != nil {
		return nil
	}
	if !FargateValid(cpu, memory) {
		return invalid("%d CPU with %d MiB is not a valid Fargate combination", cpu, memory)
	}
	return nil
}

// fargateSizes is the AWS table of task CPU units to allowed memory (MiB).
var fargateSizes = map[int][]int{
	256:   {512, 1024, 2048},
	512:   stepped(1024, 4096, 1024),
	1024:  stepped(2048, 8192, 1024),
	2048:  stepped(4096, 16384, 1024),
	4096:  stepped(8192, 30720, 1024),
	8192:  stepped(16384, 61440, 4096),
	16384: stepped(32768, 122880, 8192),
}

func stepped(min, max, step int) []int {
	var values []int
	for value := min; value <= max; value += step {
		values = append(values, value)
	}
	return values
}

// FargateValid reports whether cpuUnits with memoryMiB is a valid Fargate task size.
func FargateValid(cpuUnits, memoryMiB int) bool {
	for _, allowed := range fargateSizes[cpuUnits] {
		if allowed == memoryMiB {
			return true
		}
	}
	return false
}

// secretScope identifies where a secret lives: account plus resource-name prefix.
type secretScope struct{ account, prefix string }

// parseSecretScope extracts account and prefix (resource name up to and including its last '/')
// from an SSM parameter or Secrets Manager ARN.
func parseSecretScope(arn string) (secretScope, bool) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[4] == "" {
		return secretScope{}, false
	}
	resource := parts[5]
	slash := strings.LastIndex(resource, "/")
	if slash < 0 {
		return secretScope{}, false
	}
	return secretScope{account: parts[4], prefix: resource[:slash+1]}, true
}

// secretReferences lists every reference ECS resolves through the execution role: container
// secrets, log driver secretOptions and private registry credentials.
func secretReferences(def Definition) []string {
	var references []string
	add := func(value any) {
		if text, _ := value.(string); text != "" {
			references = append(references, text)
		}
	}
	containers, _ := def["containerDefinitions"].([]any)
	for _, entry := range containers {
		container, _ := entry.(map[string]any)
		secrets, _ := container["secrets"].([]any)
		for _, secretEntry := range secrets {
			secret, _ := secretEntry.(map[string]any)
			add(secret["valueFrom"])
		}
		logConfig, _ := container["logConfiguration"].(map[string]any)
		options, _ := logConfig["secretOptions"].([]any)
		for _, optionEntry := range options {
			option, _ := optionEntry.(map[string]any)
			add(option["valueFrom"])
		}
		credentials, _ := container["repositoryCredentials"].(map[string]any)
		add(credentials["credentialsParameter"])
	}
	return references
}

// validateSecrets lets a non-admin add only secrets in the draft's own account whose prefix
// already appears in the base definition. Removing or keeping secrets is always allowed.
func validateSecrets(base, draft Definition, accountID string) error {
	existing := map[string]bool{}
	allowedPrefixes := map[string]bool{}
	for _, reference := range secretReferences(base) {
		existing[reference] = true
		if scope, ok := parseSecretScope(reference); ok {
			allowedPrefixes[scope.prefix] = true
		}
	}
	for _, reference := range secretReferences(draft) {
		if existing[reference] {
			continue
		}
		scope, ok := parseSecretScope(reference)
		if !ok || scope.account != accountID || !allowedPrefixes[scope.prefix] {
			return invalid("Changing secrets outside the existing prefixes needs a global admin")
		}
	}
	return nil
}

// executionFetch is one kind of reference the execution role or ECS agent dereferences besides
// secrets: environment files, FSx credentials, a FireLens config file and credential specs.
type executionFetch struct {
	message    string
	references func(def Definition) []string
}

var executionFetches = []executionFetch{
	{"Changing environment files needs a global admin", func(def Definition) []string {
		return collectContainerValues(def, func(container map[string]any, add func(any)) {
			files, _ := container["environmentFiles"].([]any)
			for _, entry := range files {
				file, _ := entry.(map[string]any)
				add(file["value"])
			}
		})
	}},
	{"Changing FSx credentials needs a global admin", func(def Definition) []string {
		var references []string
		volumes, _ := def["volumes"].([]any)
		for _, entry := range volumes {
			volume, _ := entry.(map[string]any)
			fsx, _ := volume["fsxWindowsFileServerVolumeConfiguration"].(map[string]any)
			authorization, _ := fsx["authorizationConfig"].(map[string]any)
			if text, _ := authorization["credentialsParameter"].(string); text != "" {
				references = append(references, text)
			}
		}
		return references
	}},
	{"Changing the FireLens config file needs a global admin", func(def Definition) []string {
		return collectContainerValues(def, func(container map[string]any, add func(any)) {
			firelens, _ := container["firelensConfiguration"].(map[string]any)
			options, _ := firelens["options"].(map[string]any)
			if options["config-file-type"] == "s3" {
				add(options["config-file-value"])
			}
		})
	}},
	{"Changing credential specs needs a global admin", func(def Definition) []string {
		return collectContainerValues(def, func(container map[string]any, add func(any)) {
			specs, _ := container["credentialSpecs"].([]any)
			for _, spec := range specs {
				add(spec)
			}
		})
	}},
}

func collectContainerValues(def Definition, visit func(container map[string]any, add func(any))) []string {
	var values []string
	add := func(value any) {
		if text, _ := value.(string); text != "" {
			values = append(values, text)
		}
	}
	containers, _ := def["containerDefinitions"].([]any)
	for _, entry := range containers {
		container, _ := entry.(map[string]any)
		visit(container, add)
	}
	return values
}

// validateExecutionFetches lets a non-admin keep these references as deployed but not add or change
// any: unlike secrets they have no prefix scoping (S3 ARNs carry no account), so any new value is
// a new thing the execution role would fetch. Removing a reference is allowed.
func validateExecutionFetches(base, draft Definition) error {
	for _, fetch := range executionFetches {
		existing := map[string]bool{}
		for _, reference := range fetch.references(base) {
			existing[reference] = true
		}
		for _, reference := range fetch.references(draft) {
			if !existing[reference] {
				return invalid("%s", fetch.message)
			}
		}
	}
	return nil
}
