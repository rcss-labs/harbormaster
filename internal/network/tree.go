package network

import (
	"sort"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// DefaultCollapseThreshold applies when Input.CollapseThreshold is not positive.
const DefaultCollapseThreshold = 150

const noSubnetLabel = "No subnet (bridge or host network mode)"

type ServiceInfo struct {
	ID       string
	Status   model.Status
	Revision int
}

type TagInfo struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

type Input struct {
	VPC               model.VPC
	Subnets           []model.Subnet // all subnets of the account x region
	Edges             []model.VPCEdge
	Tasks             []model.Task // visible RUNNING tasks of the account x region
	Services          map[model.ServiceKey]ServiceInfo
	ClusterTags       map[model.ClusterKey]TagInfo
	CollapseThreshold int // <= 0 means DefaultCollapseThreshold (150)
}

// Node is one tree node; Kind decides which data block is set.
type Node struct {
	Kind     string       `json:"kind"` // vpc | az | nosubnet | subnet | cluster | service | standalone | task
	Key      string       `json:"key"`
	Label    string       `json:"label"`
	Children []Node       `json:"children,omitempty"`
	VPC      *VPCData     `json:"vpc,omitempty"`
	AZ       *AZData      `json:"az,omitempty"`
	Subnet   *SubnetData  `json:"subnet,omitempty"`
	Cluster  *ClusterData `json:"cluster,omitempty"`
	Service  *ServiceData `json:"service,omitempty"`
	Task     *TaskData    `json:"task,omitempty"`
	Tasks    int          `json:"tasks"`
}

type VPCData struct {
	VPCID string `json:"vpc_id"`
	CIDR  string `json:"cidr"`
	Zones int    `json:"zones"`
}

type AZData struct {
	Subnets int `json:"subnets"`
}

type SubnetData struct {
	SubnetID string `json:"subnet_id"`
	CIDR     string `json:"cidr"`
	Public   bool   `json:"public"`
	IPsUsed  int    `json:"ips_used"`
	IPsTotal int    `json:"ips_total"`
}

type ClusterData struct {
	ClusterID string  `json:"cluster_id"`
	Tag       TagInfo `json:"tag"`
	Services  int     `json:"services"`
}

type ServiceData struct {
	ServiceID string       `json:"service_id"`
	Status    model.Status `json:"status"`
	Revision  int          `json:"revision"`
}

type TaskData struct {
	TaskID    string `json:"task_id"`
	ServiceID string `json:"service_id"`
	PrivateIP string `json:"private_ip"`
	Group     string `json:"group"`
	Unhealthy bool   `json:"unhealthy"`
}

type Edge struct {
	Kind     model.VPCEdgeKind `json:"kind"`
	ID       string            `json:"id"`
	SubnetID string            `json:"subnet_id,omitempty"`
	Detail   string            `json:"detail,omitempty"`
}

type Summary struct {
	Subnets            int  `json:"subnets"`
	Zones              int  `json:"zones"`
	RunningTasks       int  `json:"running_tasks"`
	UnplacedTasks      int  `json:"unplaced_tasks"`
	CollapsedByDefault bool `json:"collapsed_by_default"`
}

type Tree struct {
	Root    Node    `json:"root"`
	Edges   []Edge  `json:"edges"`
	Summary Summary `json:"summary"`
}

// Assemble builds the VPC tree from this VPC's subnets and the visible
// running tasks that belong to it.
func Assemble(in Input) Tree {
	vpcID := in.VPC.VPCID
	threshold := in.CollapseThreshold
	if threshold <= 0 {
		threshold = DefaultCollapseThreshold
	}
	place := newPlacement(in.Subnets, in.Tasks)

	tasksBySubnet := map[string][]model.Task{}
	var noSubnetTasks []model.Task
	placed, unplaced := 0, 0
	for _, task := range in.Tasks {
		vpcs := place.vpcsOf(task)
		if len(vpcs) == 0 {
			unplaced++
			continue
		}
		if !contains(vpcs, vpcID) {
			continue
		}
		placed++
		if task.SubnetID == "" {
			noSubnetTasks = append(noSubnetTasks, task)
		} else {
			tasksBySubnet[task.SubnetID] = append(tasksBySubnet[task.SubnetID], task)
		}
	}

	var vpcSubnets []model.Subnet
	for _, subnet := range in.Subnets {
		if subnet.VPCID == vpcID {
			vpcSubnets = append(vpcSubnets, subnet)
		}
	}
	sort.SliceStable(vpcSubnets, func(i, j int) bool {
		if vpcSubnets[i].Name != vpcSubnets[j].Name {
			return vpcSubnets[i].Name < vpcSubnets[j].Name
		}
		return vpcSubnets[i].SubnetID < vpcSubnets[j].SubnetID
	})
	subnetsByAZ := map[string][]model.Subnet{}
	var zones []string
	for _, subnet := range vpcSubnets {
		if _, seen := subnetsByAZ[subnet.AZ]; !seen {
			zones = append(zones, subnet.AZ)
		}
		subnetsByAZ[subnet.AZ] = append(subnetsByAZ[subnet.AZ], subnet)
	}
	sort.Strings(zones)

	root := Node{Kind: "vpc", Key: "vpc:" + vpcID, Label: labelOr(in.VPC.Name, vpcID)}
	for _, zone := range zones {
		zoneNode := Node{Kind: "az", Key: "az:" + vpcID + ":" + zone, Label: zone, AZ: &AZData{Subnets: len(subnetsByAZ[zone])}}
		for _, subnet := range subnetsByAZ[zone] {
			used, total := SubnetIPs(subnet.CIDR, subnet.AvailableIPs)
			subnetKey := "sn:" + subnet.SubnetID
			subnetNode := Node{
				Kind:     "subnet",
				Key:      subnetKey,
				Label:    labelOr(subnet.Name, subnet.SubnetID),
				Subnet:   &SubnetData{SubnetID: subnet.SubnetID, CIDR: subnet.CIDR, Public: subnet.Public, IPsUsed: used, IPsTotal: total},
				Children: buildClusters(subnetKey, tasksBySubnet[subnet.SubnetID], in),
			}
			subnetNode.Tasks = sumTasks(subnetNode.Children)
			zoneNode.Children = append(zoneNode.Children, subnetNode)
			zoneNode.Tasks += subnetNode.Tasks
		}
		root.Children = append(root.Children, zoneNode)
		root.Tasks += zoneNode.Tasks
	}
	if len(noSubnetTasks) > 0 {
		noSubnetKey := "nosub:" + vpcID
		node := Node{Kind: "nosubnet", Key: noSubnetKey, Label: noSubnetLabel, Children: buildClusters(noSubnetKey, noSubnetTasks, in)}
		node.Tasks = sumTasks(node.Children)
		root.Children = append(root.Children, node)
		root.Tasks += node.Tasks
	}
	root.VPC = &VPCData{VPCID: vpcID, CIDR: in.VPC.CIDR, Zones: len(zones)}

	return Tree{
		Root:  root,
		Edges: sortedEdges(in.Edges),
		Summary: Summary{
			Subnets:            len(vpcSubnets),
			Zones:              len(zones),
			RunningTasks:       placed,
			UnplacedTasks:      unplaced,
			CollapsedByDefault: placed > threshold,
		},
	}
}

// buildClusters groups tasks into cluster > service > task nodes under prefix.
func buildClusters(prefix string, tasks []model.Task, in Input) []Node {
	if len(tasks) == 0 {
		return nil
	}
	byCluster := map[model.ClusterKey][]model.Task{}
	for _, task := range tasks {
		byCluster[task.Cluster] = append(byCluster[task.Cluster], task)
	}
	clusterKeys := make([]model.ClusterKey, 0, len(byCluster))
	for key := range byCluster {
		clusterKeys = append(clusterKeys, key)
	}
	sort.Slice(clusterKeys, func(i, j int) bool { return clusterKeys[i].Cluster < clusterKeys[j].Cluster })

	clusterNodes := make([]Node, 0, len(clusterKeys))
	for _, clusterKey := range clusterKeys {
		clusterPrefix := prefix + ":" + clusterKey.Cluster
		byService := map[string][]model.Task{}
		var standalone []model.Task
		for _, task := range byCluster[clusterKey] {
			if task.ServiceName == "" {
				standalone = append(standalone, task)
			} else {
				byService[task.ServiceName] = append(byService[task.ServiceName], task)
			}
		}
		serviceNames := make([]string, 0, len(byService))
		for name := range byService {
			serviceNames = append(serviceNames, name)
		}
		sort.Strings(serviceNames)

		clusterNode := Node{
			Kind:    "cluster",
			Key:     clusterPrefix,
			Label:   clusterKey.Cluster,
			Cluster: &ClusterData{ClusterID: clusterKey.ID(), Tag: in.ClusterTags[clusterKey], Services: len(serviceNames)},
		}
		for _, name := range serviceNames {
			key := model.ServiceKey{ClusterKey: clusterKey, Service: name}
			info := in.Services[key]
			serviceNode := Node{
				Kind:    "service",
				Key:     clusterPrefix + ":" + name,
				Label:   name,
				Service: &ServiceData{ServiceID: key.ID(), Status: info.Status, Revision: info.Revision},
			}
			serviceNode.Children = taskNodes(byService[name], key.ID())
			serviceNode.Tasks = len(serviceNode.Children)
			clusterNode.Children = append(clusterNode.Children, serviceNode)
		}
		if len(standalone) > 0 {
			standaloneNode := Node{Kind: "standalone", Key: clusterPrefix + ":_standalone", Label: "Standalone tasks"}
			standaloneNode.Children = taskNodes(standalone, "")
			standaloneNode.Tasks = len(standaloneNode.Children)
			clusterNode.Children = append(clusterNode.Children, standaloneNode)
		}
		clusterNode.Tasks = sumTasks(clusterNode.Children)
		clusterNodes = append(clusterNodes, clusterNode)
	}
	return clusterNodes
}

// taskNodes orders tasks by StartedAt then ARN.
func taskNodes(tasks []model.Task, serviceID string) []Node {
	sorted := append([]model.Task(nil), tasks...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		switch {
		case a.StartedAt != nil && b.StartedAt != nil && !a.StartedAt.Equal(*b.StartedAt):
			return a.StartedAt.Before(*b.StartedAt)
		case (a.StartedAt == nil) != (b.StartedAt == nil):
			return a.StartedAt == nil // unknown start time first
		}
		return a.ARN < b.ARN
	})
	nodes := make([]Node, 0, len(sorted))
	for _, task := range sorted {
		taskID := model.TaskID(task.Cluster.AccountID, task.Cluster.Region, task.ARN)
		group := task.Group
		if group == "" {
			group = task.StartedBy
		}
		nodes = append(nodes, Node{
			Kind:  "task",
			Key:   "task:" + taskID,
			Label: ShortTaskID(task.ARN),
			Tasks: 1,
			Task: &TaskData{
				TaskID:    taskID,
				ServiceID: serviceID,
				PrivateIP: task.PrivateIP,
				Group:     group,
				Unhealthy: task.HealthStatus == "UNHEALTHY",
			},
		})
	}
	return nodes
}

func sortedEdges(edges []model.VPCEdge) []Edge {
	rank := map[model.VPCEdgeKind]int{model.VPCEdgeIGW: 0, model.VPCEdgeNAT: 1, model.VPCEdgeEndpoint: 2}
	out := make([]Edge, 0, len(edges))
	for _, edge := range edges {
		out = append(out, Edge{Kind: edge.Kind, ID: edge.ResourceID, SubnetID: edge.SubnetID, Detail: edge.Detail})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if rank[a.Kind] != rank[b.Kind] {
			return rank[a.Kind] < rank[b.Kind]
		}
		if a.Detail != b.Detail {
			return a.Detail < b.Detail
		}
		return a.ID < b.ID
	})
	return out
}

func sumTasks(nodes []Node) int {
	total := 0
	for _, node := range nodes {
		total += node.Tasks
	}
	return total
}

func labelOr(label, fallback string) string {
	if label == "" {
		return fallback
	}
	return label
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
