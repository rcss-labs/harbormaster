package storage

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/model"
)

func TestMountsFromTaskDefinition_EFSVolumeWithMountPoint(t *testing.T) {
	raw, err := os.ReadFile("testdata/taskdef_efs.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := MountsFromTaskDefinition(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []Mount{{FileSystemID: "fs-02e7d1c9", AccessPointID: "fsap-07f3e9a1c2", Container: "report-worker", ContainerPath: "/data/reports", ReadOnly: false}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mounts = %+v, want %+v", got, want)
	}
}

func TestMountsFromTaskDefinition_IgnoresNonEFSVolumes(t *testing.T) {
	raw := `{"containerDefinitions":[{"name":"a","mountPoints":[{"sourceVolume":"h","containerPath":"/h"},{"sourceVolume":"d","containerPath":"/d"}]}],
	"volumes":[{"name":"h","host":{"sourcePath":"/tmp"}},{"name":"d","dockerVolumeConfiguration":{"scope":"task"}}]}`
	got, err := MountsFromTaskDefinition(json.RawMessage(raw))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v; want none", got, err)
	}
}

func TestMountsFromTaskDefinition_VolumeWithoutMountPointIsSkipped(t *testing.T) {
	raw := `{"containerDefinitions":[{"name":"a"}],"volumes":[{"name":"v","efsVolumeConfiguration":{"fileSystemId":"fs-1"}}]}`
	got, err := MountsFromTaskDefinition(json.RawMessage(raw))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v; want none", got, err)
	}
}

func TestMountsFromTaskDefinition_TwoContainersSameVolume(t *testing.T) {
	raw := `{"containerDefinitions":[
	{"name":"a","mountPoints":[{"sourceVolume":"v","containerPath":"/a"}]},
	{"name":"b","mountPoints":[{"sourceVolume":"v","containerPath":"/b","readOnly":true}]}],
	"volumes":[{"name":"v","efsVolumeConfiguration":{"fileSystemId":"fs-1"}}]}`
	got, err := MountsFromTaskDefinition(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := []Mount{
		{FileSystemID: "fs-1", Container: "a", ContainerPath: "/a"},
		{FileSystemID: "fs-1", Container: "b", ContainerPath: "/b", ReadOnly: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mounts = %+v, want %+v", got, want)
	}
}

func TestMountsFromTaskDefinition_InvalidJSON(t *testing.T) {
	if _, err := MountsFromTaskDefinition(json.RawMessage(`{not json`)); err == nil {
		t.Fatal("want error")
	}
}

func efsTaskDef(fsID, path string) json.RawMessage {
	return json.RawMessage(`{"containerDefinitions":[{"name":"c","mountPoints":[{"sourceVolume":"v","containerPath":"` + path + `"}]}],
	"volumes":[{"name":"v","efsVolumeConfiguration":{"fileSystemId":"` + fsID + `"}}]}`)
}

func svc(cluster, name, td string) model.Service {
	return model.Service{Key: model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "111111111111", Region: "us-east-1", Cluster: cluster}, Service: name}, TaskDefinitionARN: td}
}

func TestMountIndex_GroupsAndSorts(t *testing.T) {
	nginx := svc("edge-proxy", "edge-nginx", "td-nginx")
	renewer := svc("edge-proxy", "cert-renewer", "td-renewer")
	orphan := svc("edge-proxy", "zz-missing", "td-gone")
	other := svc("background-jobs", "report-worker", "td-report")
	early := svc("archive", "zz-sync", "td-sync") // earlier cluster, later name
	taskDefs := map[string]json.RawMessage{
		"td-nginx": efsTaskDef("fs-0a6b33f0", "/etc/nginx/certs"), "td-renewer": efsTaskDef("fs-0a6b33f0", "/certs"),
		"td-report": efsTaskDef("fs-02e7d1c9", "/data/reports"), "td-sync": efsTaskDef("fs-0a6b33f0", "/sync"),
	}
	running := map[model.ServiceKey]int{nginx.Key: 2, renewer.Key: 1}
	idx := MountIndex([]model.Service{nginx, orphan, renewer, other, early}, taskDefs, running)

	got := idx["fs-0a6b33f0"]
	if len(got) != 3 || got[0].ServiceName != "zz-sync" || got[1].ServiceName != "cert-renewer" || got[2].ServiceName != "edge-nginx" {
		t.Fatalf("fs-0a6b33f0 = %+v, want zz-sync (cluster archive), cert-renewer, edge-nginx", got)
	}
	if got[1].RunningTasks != 1 || got[2].RunningTasks != 2 || got[2].ContainerPath != "/etc/nginx/certs" || got[1].ServiceID != renewer.Key.ID() || got[1].Cluster != "edge-proxy" {
		t.Fatalf("unexpected fields: %+v", got)
	}
	if len(idx["fs-02e7d1c9"]) != 1 {
		t.Fatalf("report-worker not indexed: %+v", idx)
	}
	for _, list := range idx {
		for _, m := range list {
			if m.ServiceName == "zz-missing" {
				t.Fatal("service without task definition must be skipped")
			}
		}
	}
}
