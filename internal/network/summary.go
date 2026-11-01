package network

import (
	"sort"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// RegionSummary is the account-level region card.
type RegionSummary struct {
	Region        string
	ActiveVPCs    int
	HiddenVPCs    int
	Clusters      int
	Tasks         int
	UnplacedTasks int
}

// VPCSummary is one active VPC card.
type VPCSummary struct {
	VPC      model.VPC
	Subnets  int
	Clusters []string
	Tasks    int
}

// ActiveVPCs counts the given (visible, running) tasks per VPC id.
func ActiveVPCs(subnets []model.Subnet, tasks []model.Task) map[string]int {
	place := newPlacement(subnets, tasks)
	counts := map[string]int{}
	for _, task := range tasks {
		for _, vpcID := range place.vpcsOf(task) {
			counts[vpcID]++
		}
	}
	return counts
}

// SummarizeRegion builds the region card. tasks are the caller's visible running tasks: they give
// the active VPCs, tasks and clusters (decision 5). hidden_vpcs counts VPCs with no running task
// at all (allTasks, unfiltered), so it is the same for every caller and leaks nothing; a VPC that
// only holds tasks the caller cannot see is neither active nor hidden.
func SummarizeRegion(region string, vpcs []model.VPC, subnets []model.Subnet, tasks, allTasks []model.Task) RegionSummary {
	place := newPlacement(subnets, tasks)
	active := ActiveVPCs(subnets, tasks)
	anyTask := ActiveVPCs(subnets, allTasks)
	summary := RegionSummary{Region: region}
	for _, vpc := range vpcs {
		switch {
		case active[vpc.VPCID] > 0:
			summary.ActiveVPCs++
		case anyTask[vpc.VPCID] == 0:
			summary.HiddenVPCs++
		}
	}
	clusters := map[model.ClusterKey]bool{}
	for _, task := range tasks {
		if len(place.vpcsOf(task)) == 0 {
			summary.UnplacedTasks++
			continue
		}
		summary.Tasks++
		clusters[task.Cluster] = true
	}
	summary.Clusters = len(clusters)
	return summary
}

// SummarizeVPCs returns the active VPCs only, sorted by name.
func SummarizeVPCs(vpcs []model.VPC, subnets []model.Subnet, tasks []model.Task) []VPCSummary {
	place := newPlacement(subnets, tasks)
	subnetCount := map[string]int{}
	for _, subnet := range subnets {
		subnetCount[subnet.VPCID]++
	}
	taskCount := map[string]int{}
	clusterNames := map[string]map[string]bool{}
	for _, task := range tasks {
		for _, vpcID := range place.vpcsOf(task) {
			taskCount[vpcID]++
			if clusterNames[vpcID] == nil {
				clusterNames[vpcID] = map[string]bool{}
			}
			clusterNames[vpcID][task.Cluster.Cluster] = true
		}
	}
	summaries := make([]VPCSummary, 0, len(vpcs))
	for _, vpc := range vpcs {
		if taskCount[vpc.VPCID] == 0 {
			continue
		}
		names := make([]string, 0, len(clusterNames[vpc.VPCID]))
		for name := range clusterNames[vpc.VPCID] {
			names = append(names, name)
		}
		sort.Strings(names)
		summaries = append(summaries, VPCSummary{VPC: vpc, Subnets: subnetCount[vpc.VPCID], Clusters: names, Tasks: taskCount[vpc.VPCID]})
	}
	sort.SliceStable(summaries, func(i, j int) bool {
		if summaries[i].VPC.Name != summaries[j].VPC.Name {
			return summaries[i].VPC.Name < summaries[j].VPC.Name
		}
		return summaries[i].VPC.VPCID < summaries[j].VPC.VPCID
	})
	return summaries
}
