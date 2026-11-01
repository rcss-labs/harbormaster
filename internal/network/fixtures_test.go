package network

import (
	"fmt"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
)

const (
	testAccount = "4821"
	testRegion  = "us-east-1"

	mainVPCID   = "vpc-0a41c7e29b3d"
	sharedVPCID = "vpc-0f5e8b7d1c44"
	legacyVPCID = "vpc-0d3f77aa01c2"

	private1a = "subnet-03f1a9c2"
	private1b = "subnet-0b77e410"
	public1a  = "subnet-09c2d5ee"
	shared1a  = "subnet-0aa1c0de"
	shared1b  = "subnet-0bb2c0de"
	legacy1a  = "subnet-0def1234"

	devTag  authz.TagID = 1
	prodTag authz.TagID = 2
)

var baseTime = time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)

func clusterKey(name string) model.ClusterKey {
	return model.ClusterKey{AccountID: testAccount, Region: testRegion, Cluster: name}
}

func serviceKey(cluster, service string) model.ServiceKey {
	return model.ServiceKey{ClusterKey: clusterKey(cluster), Service: service}
}

// fakeTags is an in-memory authz.Tags.
type fakeTags struct {
	clusters map[model.ClusterKey]authz.TagID
	services map[model.ServiceKey]authz.TagID
}

func (f fakeTags) DevTagID() authz.TagID { return devTag }
func (f fakeTags) ClusterTag(k model.ClusterKey) (authz.TagID, bool) {
	id, ok := f.clusters[k]
	return id, ok
}
func (f fakeTags) ServiceTag(k model.ServiceKey) (authz.TagID, bool) {
	id, ok := f.services[k]
	return id, ok
}

// testTags: core-services is Prod, except auth-service which carries its own
// Dev tag; everything else falls back to Dev.
func testTags() fakeTags {
	return fakeTags{
		clusters: map[model.ClusterKey]authz.TagID{clusterKey("core-services"): prodTag},
		services: map[model.ServiceKey]authz.TagID{serviceKey("core-services", "auth-service"): devTag},
	}
}

func subjectWith(tag authz.TagID) authz.Subject {
	return authz.Subject{UserID: 7, Username: "u", Perms: map[authz.TagID]authz.ActionSet{tag: authz.ActionSet(authz.View)}}
}

// runningTask builds a RUNNING task; startedMinute orders tasks by StartedAt.
func runningTask(cluster, service, subnet, ip string, startedMinute int) model.Task {
	started := baseTime.Add(time.Duration(startedMinute) * time.Minute)
	az := ""
	switch subnet {
	case private1a, public1a, shared1a, legacy1a:
		az = "us-east-1a"
	case private1b, shared1b:
		az = "us-east-1b"
	}
	return model.Task{
		Cluster:          clusterKey(cluster),
		ARN:              fmt.Sprintf("arn:aws:ecs:%s:%s:task/%s/%08x1c2d3e4f5a6b", testRegion, testAccount, cluster, 0x6faba45f+startedMinute),
		ServiceName:      service,
		LastStatus:       "RUNNING",
		HealthStatus:     "HEALTHY",
		AvailabilityZone: az,
		SubnetID:         subnet,
		PrivateIP:        ip,
		StartedAt:        &started,
	}
}

func testVPCs() []model.VPC {
	return []model.VPC{
		{AccountID: testAccount, Region: testRegion, VPCID: mainVPCID, Name: "homelab-main", CIDR: "10.20.0.0/16"},
		{AccountID: testAccount, Region: testRegion, VPCID: sharedVPCID, Name: "shared-services", CIDR: "10.30.0.0/16"},
		{AccountID: testAccount, Region: testRegion, VPCID: legacyVPCID, Name: "legacy-default", CIDR: "172.31.0.0/16", IsDefault: true},
	}
}

func testSubnets() []model.Subnet {
	return []model.Subnet{
		// us-east-1b first, so the AZ order must come from sorting.
		{SubnetID: private1b, VPCID: mainVPCID, Name: "private-1b", CIDR: "10.20.2.0/24", AZ: "us-east-1b", AvailableIPs: 233},
		{SubnetID: public1a, VPCID: mainVPCID, Name: "public-1a", CIDR: "10.20.101.0/24", AZ: "us-east-1a", AvailableIPs: 248, Public: true},
		{SubnetID: private1a, VPCID: mainVPCID, Name: "private-1a", CIDR: "10.20.1.0/24", AZ: "us-east-1a", AvailableIPs: 224},
		{SubnetID: shared1a, VPCID: sharedVPCID, Name: "shared-1a", CIDR: "10.30.1.0/24", AZ: "us-east-1a", AvailableIPs: 240},
		{SubnetID: shared1b, VPCID: sharedVPCID, Name: "shared-1b", CIDR: "10.30.2.0/24", AZ: "us-east-1b", AvailableIPs: 241},
		{SubnetID: legacy1a, VPCID: legacyVPCID, Name: "legacy-a", CIDR: "172.31.0.0/20", AZ: "us-east-1a", AvailableIPs: 4091},
	}
}

// mainTasks are the 10 running tasks of homelab-main, as in network-tree.png:
// private-1a 7 (core-services 5 across 4 services, background-jobs 2 across
// 2 services), private-1b 3 (core-services, 3 services). public-1a is empty.
func mainTasks() []model.Task {
	return []model.Task{
		runningTask("core-services", "payments-api", private1a, "10.20.1.23", 1),
		runningTask("core-services", "payments-api", private1a, "10.20.1.24", 2),
		runningTask("core-services", "web-frontend", private1a, "10.20.1.25", 3),
		runningTask("core-services", "auth-service", private1a, "10.20.1.26", 4),
		runningTask("core-services", "notifications", private1a, "10.20.1.27", 5),
		runningTask("background-jobs", "report-worker", private1a, "10.20.1.50", 6),
		runningTask("background-jobs", "email-sender", private1a, "10.20.1.51", 7),
		runningTask("core-services", "payments-api", private1b, "10.20.2.20", 8),
		runningTask("core-services", "web-frontend", private1b, "10.20.2.21", 9),
		runningTask("core-services", "auth-service", private1b, "10.20.2.22", 10),
	}
}

// standaloneTask is a service-less task of background-jobs in private-1a.
func standaloneTask() model.Task {
	task := runningTask("background-jobs", "", private1a, "10.20.1.60", 20)
	task.Group = "family:report-backfill"
	return task
}

// sharedTasks are the 2 running tasks of shared-services (cluster ci-tools).
func sharedTasks() []model.Task {
	build := runningTask("ci-tools", "", shared1a, "10.30.1.10", 11)
	build.Group = "family:release-build"
	return []model.Task{
		build,
		runningTask("ci-tools", "runner", shared1b, "10.30.2.10", 12),
	}
}

func allTasks() []model.Task { return append(mainTasks(), sharedTasks()...) }

func mainInput() Input {
	return Input{
		VPC:     testVPCs()[0],
		Subnets: testSubnets(),
		Tasks:   mainTasks(),
		Services: map[model.ServiceKey]ServiceInfo{
			serviceKey("core-services", "payments-api"):    {ID: serviceKey("core-services", "payments-api").ID(), Status: model.StatusSteady, Revision: 42},
			serviceKey("core-services", "web-frontend"):    {ID: serviceKey("core-services", "web-frontend").ID(), Status: model.StatusSteady, Revision: 87},
			serviceKey("core-services", "auth-service"):    {ID: serviceKey("core-services", "auth-service").ID(), Status: model.StatusSteady, Revision: 15},
			serviceKey("core-services", "notifications"):   {ID: serviceKey("core-services", "notifications").ID(), Status: model.StatusDeploying, Revision: 9},
			serviceKey("background-jobs", "report-worker"): {ID: serviceKey("background-jobs", "report-worker").ID(), Status: model.StatusSteady, Revision: 31},
			serviceKey("background-jobs", "email-sender"):  {ID: serviceKey("background-jobs", "email-sender").ID(), Status: model.StatusSteady, Revision: 12},
		},
		ClusterTags: map[model.ClusterKey]TagInfo{
			clusterKey("core-services"):   {Name: "Prod", Color: "red"},
			clusterKey("background-jobs"): {Name: "Dev", Color: "blue"},
		},
		CollapseThreshold: 150,
	}
}

// findNode walks the tree depth-first for the node with the given key.
func findNode(root Node, key string) (Node, bool) {
	if root.Key == key {
		return root, true
	}
	for _, child := range root.Children {
		if found, ok := findNode(child, key); ok {
			return found, true
		}
	}
	return Node{}, false
}

func mustFind(t interface {
	Helper()
	Fatalf(string, ...any)
}, root Node, key string) Node {
	t.Helper()
	node, ok := findNode(root, key)
	if !ok {
		t.Fatalf("node %q not found", key)
	}
	return node
}

func childKeys(node Node) []string {
	keys := make([]string, 0, len(node.Children))
	for _, child := range node.Children {
		keys = append(keys, child.Key)
	}
	return keys
}
