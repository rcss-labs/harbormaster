package deploy

import (
	"maps"
	"reflect"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
)

// DefinitionImages reads the images a submitted task definition would register: container name to image string, and the
// platform the tasks run on as "os/arch" ("linux/amd64" without a runtimePlatform).
//
// It follows Prepare up to the canonical form: the same folded-key check and strict decode, then canonicalDefinition.
// Reading the submitted map directly would miss {"ContainerDefinitions": [{"Image": ...}]}, which encoding/json decodes
// into the request all the same, so the image security gate would judge a different image than the one registered.
// The error is the *ValidationError Prepare gives for the same input.
func DefinitionImages(def Definition) (images map[string]string, platform string, err error) {
	canonical, err := canonicalOf(def)
	if err != nil {
		return nil, "", err
	}
	containers, _ := canonical["containerDefinitions"].([]any)
	// Two entries with one name would collapse into one map entry and hide an image from the gate. Prepare refuses the
	// same input, so the error here only means the apply answers 422.
	if name := duplicateContainerName(containers); name != "" {
		return nil, "", invalid("Task definition JSON is invalid: container name %q is used more than once", name)
	}
	images = make(map[string]string, len(containers))
	for _, entry := range containers {
		container, _ := entry.(map[string]any)
		name, _ := container["name"].(string)
		image, _ := container["image"].(string)
		images[name] = image
	}
	return images, platformOf(canonical), nil
}

// DefinitionFamily is the family of a submitted definition, read from the same canonical form as DefinitionImages.
func DefinitionFamily(def Definition) (string, error) {
	canonical, err := canonicalOf(def)
	if err != nil {
		return "", err
	}
	family, _ := canonical["family"].(string)
	return family, nil
}

// canonicalOf is Prepare's path up to the canonical form: folded-key check, strict decode, canonicalDefinition.
func canonicalOf(def Definition) (Definition, error) {
	if err := checkFoldedKeys(map[string]any(def), reflect.TypeOf(ecs.RegisterTaskDefinitionInput{}), ""); err != nil {
		return nil, err
	}
	// Prepare rewrites numeric cpu and memory in place; do it on a copy so a read never edits the caller's map.
	draft := maps.Clone(def)
	for _, key := range []string{"cpu", "memory"} {
		if err := normalizeSizeField(draft, key); err != nil {
			return nil, err
		}
	}
	input, err := decodeDefinition(draft, nil, true)
	if err != nil {
		return nil, invalidDecode(err)
	}
	canonical, err := canonicalDefinition(input)
	if err != nil {
		return nil, invalid("Task definition JSON is invalid: %s", err)
	}
	return canonical, nil
}

// platformOf is "os/arch" from the canonical runtimePlatform. Only ARM64 and Windows change the default.
func platformOf(canonical Definition) string {
	osName, arch := "linux", "amd64"
	if runtime, ok := canonical["runtimePlatform"].(map[string]any); ok {
		if family, _ := runtime["operatingSystemFamily"].(string); strings.HasPrefix(family, "WINDOWS") {
			osName = "windows"
		}
		if cpu, _ := runtime["cpuArchitecture"].(string); cpu == "ARM64" {
			arch = "arm64"
		}
	}
	return osName + "/" + arch
}
