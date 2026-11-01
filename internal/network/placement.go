package network

import (
	"sort"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// placement maps tasks to the VPCs whose trees contain them. It is shared by
// Assemble and the summaries so counts stay consistent (plan 05 decision 3).
type placement struct {
	// vpcOfSubnet maps subnet id to VPC id.
	vpcOfSubnet map[string]string
	// clusterVPCs lists, per cluster, the VPCs where it has an awsvpc task.
	clusterVPCs map[model.ClusterKey][]string
}

func newPlacement(subnets []model.Subnet, tasks []model.Task) placement {
	p := placement{
		vpcOfSubnet: make(map[string]string, len(subnets)),
		clusterVPCs: map[model.ClusterKey][]string{},
	}
	for _, subnet := range subnets {
		p.vpcOfSubnet[subnet.SubnetID] = subnet.VPCID
	}
	seen := map[model.ClusterKey]map[string]bool{}
	for _, task := range tasks {
		vpcID, known := p.vpcOfSubnet[task.SubnetID]
		if task.SubnetID == "" || !known {
			continue
		}
		if seen[task.Cluster] == nil {
			seen[task.Cluster] = map[string]bool{}
		}
		if !seen[task.Cluster][vpcID] {
			seen[task.Cluster][vpcID] = true
			p.clusterVPCs[task.Cluster] = append(p.clusterVPCs[task.Cluster], vpcID)
		}
	}
	for _, vpcs := range p.clusterVPCs {
		sort.Strings(vpcs)
	}
	return p
}

// vpcsOf returns the VPC ids a task belongs to; empty means unplaced.
func (p placement) vpcsOf(task model.Task) []string {
	if task.SubnetID == "" {
		return p.clusterVPCs[task.Cluster]
	}
	if vpcID, ok := p.vpcOfSubnet[task.SubnetID]; ok {
		return []string{vpcID}
	}
	return nil
}
