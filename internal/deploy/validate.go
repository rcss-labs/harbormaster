package deploy

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ValidationError is a user-correctable problem with a task definition draft; the API maps it to 422 "invalid".
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

// Validate checks a draft against the definition it was edited from (spec §7.1).
// It returns nil or a *ValidationError. It mutates draft only to normalize numeric
// cpu/memory values to strings, the form RegisterTaskDefinition expects.
func Validate(base, draft Definition, admin bool, accountID string) error {
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

	normalizeSizeField(draft, "cpu")
	normalizeSizeField(draft, "memory")
	if err := validateFargateSize(draft); err != nil {
		return err
	}

	if admin {
		return nil
	}
	if draft["taskRoleArn"] != base["taskRoleArn"] || draft["executionRoleArn"] != base["executionRoleArn"] {
		return invalid("Changing roles needs a global admin")
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
func normalizeSizeField(def Definition, key string) {
	switch value := def[key].(type) {
	case float64:
		def[key] = strconv.FormatInt(int64(value), 10)
	case int:
		def[key] = strconv.Itoa(value)
	case int64:
		def[key] = strconv.FormatInt(value, 10)
	case json.Number:
		def[key] = value.String()
	}
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

type memoryRange struct{ min, max, step int }

// fargateSizes is the AWS table of task CPU units to allowed memory (MiB).
var fargateSizes = map[int]memoryRange{
	256:   {512, 2048, 512},
	512:   {1024, 4096, 1024},
	1024:  {2048, 8192, 1024},
	2048:  {4096, 16384, 1024},
	4096:  {8192, 30720, 1024},
	8192:  {16384, 61440, 4096},
	16384: {32768, 122880, 8192},
}

// FargateValid reports whether cpuUnits with memoryMiB is a valid Fargate task size.
func FargateValid(cpuUnits, memoryMiB int) bool {
	allowed, ok := fargateSizes[cpuUnits]
	if !ok || memoryMiB < allowed.min || memoryMiB > allowed.max {
		return false
	}
	return (memoryMiB-allowed.min)%allowed.step == 0
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

func secretReferences(def Definition) []string {
	var references []string
	containers, _ := def["containerDefinitions"].([]any)
	for _, entry := range containers {
		container, _ := entry.(map[string]any)
		secrets, _ := container["secrets"].([]any)
		for _, secretEntry := range secrets {
			secret, _ := secretEntry.(map[string]any)
			if valueFrom, _ := secret["valueFrom"].(string); valueFrom != "" {
				references = append(references, valueFrom)
			}
		}
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
