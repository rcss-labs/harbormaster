// Package logs reads container logs from CloudWatch Logs: task-definition log
// configuration, stream naming, filter patterns and backwards history paging.
package logs

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// LogConfig is the log setup of one container. Driver is "" when the
// container has no logConfiguration.
type LogConfig struct{ Container, Driver, Group, Region, StreamPrefix string }

// ConfigsFromTaskDefinition returns one LogConfig per container definition, in
// order. It parses td.Raw (the SDK task definition as the poller stores it)
// and falls back to the already-parsed td.Containers when Raw has no containers.
func ConfigsFromTaskDefinition(td model.TaskDefinitionRef) ([]LogConfig, error) {
	if len(td.Raw) > 0 {
		var def ecstypes.TaskDefinition
		if err := json.Unmarshal(td.Raw, &def); err != nil {
			return nil, fmt.Errorf("parse task definition %s: %w", td.ARN, err)
		}
		if len(def.ContainerDefinitions) > 0 {
			configs := make([]LogConfig, 0, len(def.ContainerDefinitions))
			for _, cd := range def.ContainerDefinitions {
				cfg := LogConfig{Container: aws.ToString(cd.Name)}
				if lc := cd.LogConfiguration; lc != nil {
					cfg.Driver = string(lc.LogDriver)
					cfg.Group = lc.Options["awslogs-group"]
					cfg.Region = lc.Options["awslogs-region"]
					cfg.StreamPrefix = lc.Options["awslogs-stream-prefix"]
				}
				configs = append(configs, cfg)
			}
			return configs, nil
		}
	}
	configs := make([]LogConfig, 0, len(td.Containers))
	for _, c := range td.Containers {
		configs = append(configs, LogConfig{
			Container: c.Name, Driver: c.LogDriver, Group: c.LogGroup, Region: c.LogRegion, StreamPrefix: c.LogStreamPrefix,
		})
	}
	return configs, nil
}

// Supported reports whether Harbormaster can read the container's logs.
func (c LogConfig) Supported() bool { return c.Driver == "awslogs" && c.Group != "" }

// StreamPrefix is the log stream name prefix of the container's streams. The
// trailing slash stops "api" from also matching a container named "api-worker".
func StreamPrefix(c LogConfig) string {
	if c.StreamPrefix == "" {
		return c.Container + "/"
	}
	return c.StreamPrefix + "/" + c.Container + "/"
}

// ParseStream splits "<prefix>/<container>/<task id>"; with fewer than three
// parts the container is unknown and only the task id (last part) is returned.
func ParseStream(name string) (container, taskID string) {
	parts := strings.Split(name, "/")
	taskID = parts[len(parts)-1]
	if len(parts) < 3 {
		return "", taskID
	}
	return parts[len(parts)-2], taskID
}

// FilterPattern turns free text into a CloudWatch filter pattern that matches
// every whitespace-separated term literally. Each term is quoted so pattern
// metacharacters ([ ] { } ? * leading -) cannot cause InvalidParameterException.
func FilterPattern(q string) string {
	terms := strings.Fields(q)
	quoted := make([]string, len(terms))
	escaper := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	for i, term := range terms {
		quoted[i] = `"` + escaper.Replace(term) + `"`
	}
	return strings.Join(quoted, " ")
}
