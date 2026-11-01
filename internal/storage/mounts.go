// Package storage maps ECS task definitions to the EFS file systems they
// mount and decides which file systems a user may see.
package storage

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// Mount is one container's mount of an EFS file system.
type Mount struct {
	FileSystemID, AccessPointID, Container, ContainerPath string
	ReadOnly                                              bool
}

// Only the fields we need are parsed, so unknown fields never break parsing.
type taskDefinitionDoc struct {
	ContainerDefinitions []struct {
		Name        string `json:"name"`
		MountPoints []struct {
			SourceVolume  string `json:"sourceVolume"`
			ContainerPath string `json:"containerPath"`
			ReadOnly      bool   `json:"readOnly"`
		} `json:"mountPoints"`
	} `json:"containerDefinitions"`
	Volumes []struct {
		Name                   string `json:"name"`
		EFSVolumeConfiguration *struct {
			FileSystemID        string `json:"fileSystemId"`
			AuthorizationConfig *struct {
				AccessPointID string `json:"accessPointId"`
			} `json:"authorizationConfig"`
		} `json:"efsVolumeConfiguration"`
	} `json:"volumes"`
}

// MountsFromTaskDefinition joins EFS volumes with the container mount points
// that reference them. Volumes nobody mounts, and non-EFS volumes, are skipped.
func MountsFromTaskDefinition(raw json.RawMessage) ([]Mount, error) {
	var doc taskDefinitionDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("storage: parse task definition: %w", err)
	}
	type efsVolume struct{ fsID, accessPointID string }
	volumes := map[string]efsVolume{}
	for _, v := range doc.Volumes {
		if v.EFSVolumeConfiguration == nil {
			continue
		}
		ev := efsVolume{fsID: v.EFSVolumeConfiguration.FileSystemID}
		if v.EFSVolumeConfiguration.AuthorizationConfig != nil {
			ev.accessPointID = v.EFSVolumeConfiguration.AuthorizationConfig.AccessPointID
		}
		volumes[v.Name] = ev
	}
	mounts := []Mount{}
	for _, c := range doc.ContainerDefinitions {
		for _, mp := range c.MountPoints {
			if ev, ok := volumes[mp.SourceVolume]; ok {
				mounts = append(mounts, Mount{FileSystemID: ev.fsID, AccessPointID: ev.accessPointID,
					Container: c.Name, ContainerPath: mp.ContainerPath, ReadOnly: mp.ReadOnly})
			}
		}
	}
	return mounts, nil
}

// MountedBy is a service that mounts a file system.
type MountedBy struct {
	Service                                        model.ServiceKey
	ServiceID, ServiceName, Cluster, ContainerPath string
	RunningTasks                                   int
}

// MountIndex maps file system ID to the services mounting it, sorted by
// cluster then service name. taskDefs is keyed by task definition ARN; a
// service whose definition is missing or unparsable is skipped. A service that
// mounts the same file system more than once appears once, with the first
// container path.
func MountIndex(services []model.Service, taskDefs map[string]json.RawMessage, running map[model.ServiceKey]int) map[string][]MountedBy {
	index := map[string][]MountedBy{}
	for _, svc := range services {
		raw, ok := taskDefs[svc.TaskDefinitionARN]
		if !ok {
			continue
		}
		mounts, err := MountsFromTaskDefinition(raw)
		if err != nil {
			continue
		}
		seen := map[string]bool{}
		for _, m := range mounts {
			if seen[m.FileSystemID] {
				continue
			}
			seen[m.FileSystemID] = true
			index[m.FileSystemID] = append(index[m.FileSystemID], MountedBy{
				Service: svc.Key, ServiceID: svc.Key.ID(), ServiceName: svc.Key.Service, Cluster: svc.Key.Cluster,
				ContainerPath: m.ContainerPath, RunningTasks: running[svc.Key],
			})
		}
	}
	for _, list := range index {
		sort.Slice(list, func(i, j int) bool {
			if list[i].Cluster != list[j].Cluster {
				return list[i].Cluster < list[j].Cluster
			}
			return list[i].ServiceName < list[j].ServiceName
		})
	}
	return index
}
