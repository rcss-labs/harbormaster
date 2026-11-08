package deploy

import (
	"maps"
	"testing"
)

func TestDefinitionImages_ReadsCanonicalForm(t *testing.T) {
	// Upper-case keys decode into the same fields encoding/json would register, so the gate must see them.
	images, platform, err := DefinitionImages(Definition{
		"Family": "web",
		"ContainerDefinitions": []any{
			map[string]any{"Name": "app", "Image": "123.dkr.ecr.eu-central-1.amazonaws.com/app:1.2"},
			map[string]any{"name": "sidecar", "image": "public.ecr.aws/x/y:v1"},
		},
	})
	if err != nil {
		t.Fatalf("DefinitionImages: %v", err)
	}
	want := map[string]string{"app": "123.dkr.ecr.eu-central-1.amazonaws.com/app:1.2", "sidecar": "public.ecr.aws/x/y:v1"}
	if !maps.Equal(images, want) {
		t.Errorf("images = %v, want %v", images, want)
	}
	if platform != "linux/amd64" {
		t.Errorf("platform without runtimePlatform = %q, want linux/amd64", platform)
	}
}

func TestDefinitionImages_PlatformFromRuntimePlatform(t *testing.T) {
	_, platform, err := DefinitionImages(Definition{
		"family":          "web",
		"runtimePlatform": map[string]any{"cpuArchitecture": "ARM64", "operatingSystemFamily": "LINUX"},
		"containerDefinitions": []any{
			map[string]any{"name": "app", "image": "a:1"},
		},
	})
	if err != nil {
		t.Fatalf("DefinitionImages: %v", err)
	}
	if platform != "linux/arm64" {
		t.Errorf("platform = %q, want linux/arm64", platform)
	}
}

func TestDefinitionImages_RejectsWhatPrepareRejects(t *testing.T) {
	for name, def := range map[string]Definition{
		"folded duplicate key": {"family": "web", "containerDefinitions": []any{
			map[string]any{"name": "app", "image": "good:1", "Image": "evil:1"}}},
		"unknown key": {"family": "web", "bogus": 1, "containerDefinitions": []any{
			map[string]any{"name": "app", "image": "a:1"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := DefinitionImages(def); err == nil {
				t.Error("DefinitionImages accepted a definition Prepare would reject")
			}
		})
	}
}
