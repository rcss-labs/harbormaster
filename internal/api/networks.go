package api

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/network"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// NetworkRegionDTO is one region card of an account on GET /api/networks.
type NetworkRegionDTO struct {
	Region        string `json:"region"`
	ActiveVPCs    int    `json:"active_vpcs"`
	HiddenVPCs    int    `json:"hidden_vpcs"`
	Clusters      int    `json:"clusters"`
	Tasks         int    `json:"tasks"`
	UnplacedTasks int    `json:"unplaced_tasks"`
}

// NetworkAccountDTO is one account with every configured region.
type NetworkAccountDTO struct {
	AccountID   string             `json:"account_id"`
	AccountName string             `json:"account_name"`
	Regions     []NetworkRegionDTO `json:"regions"`
}

// NetworksOverviewDTO is the body of GET /api/networks.
type NetworksOverviewDTO struct {
	Items    []NetworkAccountDTO `json:"items"`
	PolledAt time.Time           `json:"polled_at"`
}

// NetworkVPCCardDTO is one active VPC on GET /api/networks/{account}/{region}.
type NetworkVPCCardDTO struct {
	VPCID    string   `json:"vpc_id"`
	Name     string   `json:"name"`
	CIDR     string   `json:"cidr"`
	Subnets  int      `json:"subnets"`
	Clusters []string `json:"clusters"`
	Tasks    int      `json:"tasks"`
}

// NetworkRegionVPCsDTO is the body of GET /api/networks/{account}/{region}.
type NetworkRegionVPCsDTO struct {
	Items    []NetworkVPCCardDTO `json:"items"`
	PolledAt time.Time           `json:"polled_at"`
}

// VPCRefHeaderDTO identifies the VPC a tree belongs to.
type VPCRefHeaderDTO struct {
	VPCID       string `json:"vpc_id"`
	Name        string `json:"name"`
	CIDR        string `json:"cidr"`
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
	Region      string `json:"region"`
}

// VPCTreeDTO is the body of GET /api/vpcs/{vpcId}/tree. Root is a network.Node (typed any because
// the node type is recursive).
type VPCTreeDTO struct {
	VPC      VPCRefHeaderDTO `json:"vpc"`
	Edges    []network.Edge  `json:"edges"`
	Summary  network.Summary `json:"summary"`
	Root     any             `json:"root"`
	PolledAt time.Time       `json:"polled_at"`
}

// networkRoutes registers the plan 05 networks read routes.
func (s *Server) networkRoutes(mux *http.ServeMux) {
	s.handle(mux, "GET /api/networks", s.listNetworks, routeOptions{})
	s.handle(mux, "GET /api/networks/{account}/{region}", s.listRegionVPCs, routeOptions{})
	s.handle(mux, "GET /api/vpcs/{vpcId}/tree", s.getVPCTree, routeOptions{})
}

// visibleTasks keeps the RUNNING tasks of rows that the caller may see.
func visibleTasks(sc scope, rows []store.TaskRow) []model.Task {
	tasks := make([]model.Task, 0, len(rows))
	for _, row := range rows {
		tasks = append(tasks, row.Task)
	}
	return network.FilterTasks(network.Visibility(sc.Subject, sc.Tags), tasks)
}

// visibleRunningTasks returns the caller's visible RUNNING tasks within f, grouped per account x region.
func (s *Server) visibleRunningTasks(ctx context.Context, sc scope, f store.TargetFilter) (map[awsTarget][]model.Task, error) {
	rows, err := s.Store.RunningTasks(ctx, f, sc.Vis)
	if err != nil {
		return nil, err
	}
	return groupByTarget(visibleTasks(sc, rows)), nil
}

// allRunningTasks returns every RUNNING task within f regardless of the caller, grouped per account x region.
// It only feeds counts that must not depend on the caller (hidden_vpcs).
func (s *Server) allRunningTasks(ctx context.Context, f store.TargetFilter) (map[awsTarget][]model.Task, error) {
	rows, err := s.Store.RunningTasks(ctx, f, store.Visibility{Admin: true})
	if err != nil {
		return nil, err
	}
	tasks := make([]model.Task, 0, len(rows))
	for _, row := range rows {
		tasks = append(tasks, row.Task)
	}
	return groupByTarget(tasks), nil
}

func groupByTarget(tasks []model.Task) map[awsTarget][]model.Task {
	byTarget := map[awsTarget][]model.Task{}
	for _, task := range tasks {
		target := awsTarget{task.Cluster.AccountID, task.Cluster.Region}
		byTarget[target] = append(byTarget[target], task)
	}
	return byTarget
}

// awsTarget is an account x region pair.
type awsTarget struct{ accountID, region string }

// newestPolledAt is the newest PolledAt among tasks, else among vpcs.
func newestPolledAt(tasks []model.Task, vpcs []model.VPC) time.Time {
	var newest time.Time
	for _, task := range tasks {
		if task.PolledAt.After(newest) {
			newest = task.PolledAt
		}
	}
	if !newest.IsZero() {
		return newest
	}
	for _, vpc := range vpcs {
		if vpc.PolledAt.After(newest) {
			newest = vpc.PolledAt
		}
	}
	return newest
}

// listNetworks answers GET /api/networks: every enabled account with every configured region.
func (s *Server) listNetworks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	accounts, err := s.Store.ListAccounts(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	byTarget, err := s.visibleRunningTasks(ctx, sc, sc.Filter)
	if err != nil {
		s.fail(w, err)
		return
	}
	everyTask, err := s.allRunningTasks(ctx, sc.Filter)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := NetworksOverviewDTO{Items: []NetworkAccountDTO{}}
	var newest time.Time
	for _, account := range accounts {
		if !account.Enabled || (sc.Filter.AccountID != "" && sc.Filter.AccountID != account.AccountID) {
			continue
		}
		item := NetworkAccountDTO{AccountID: account.AccountID, AccountName: account.Name, Regions: []NetworkRegionDTO{}}
		for _, region := range account.Regions {
			if sc.Filter.Region != "" && sc.Filter.Region != region {
				continue
			}
			vpcs, err := s.Store.ListVPCs(ctx, account.AccountID, region)
			if err != nil {
				s.fail(w, err)
				return
			}
			subnets, err := s.Store.ListSubnets(ctx, account.AccountID, region)
			if err != nil {
				s.fail(w, err)
				return
			}
			tasks := byTarget[awsTarget{account.AccountID, region}]
			summary := network.SummarizeRegion(region, vpcs, subnets, tasks, everyTask[awsTarget{account.AccountID, region}])
			item.Regions = append(item.Regions, NetworkRegionDTO{
				Region: region, ActiveVPCs: summary.ActiveVPCs, HiddenVPCs: summary.HiddenVPCs,
				Clusters: summary.Clusters, Tasks: summary.Tasks, UnplacedTasks: summary.UnplacedTasks,
			})
			if polled := newestPolledAt(tasks, vpcs); polled.After(newest) {
				newest = polled
			}
		}
		out.Items = append(out.Items, item)
	}
	out.PolledAt = newest
	writeJSON(w, http.StatusOK, out)
}

// findAccountRegion resolves an enabled account and one of its configured regions; anything else is 404.
func (s *Server) findAccountRegion(ctx context.Context, accountID, region string) (store.AWSAccount, error) {
	accounts, err := s.Store.ListAccounts(ctx)
	if err != nil {
		return store.AWSAccount{}, err
	}
	for _, account := range accounts {
		if account.Enabled && account.AccountID == accountID && slices.Contains(account.Regions, region) {
			return account, nil
		}
	}
	return store.AWSAccount{}, store.ErrNotFound
}

// listRegionVPCs answers GET /api/networks/{account}/{region}: the active VPCs the caller can see.
func (s *Server) listRegionVPCs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	account, err := s.findAccountRegion(ctx, r.PathValue("account"), r.PathValue("region"))
	if err != nil {
		s.fail(w, err)
		return
	}
	region := r.PathValue("region")
	target := store.TargetFilter{AccountID: account.AccountID, Region: region}
	byTarget, err := s.visibleRunningTasks(ctx, sc, target)
	if err != nil {
		s.fail(w, err)
		return
	}
	vpcs, err := s.Store.ListVPCs(ctx, account.AccountID, region)
	if err != nil {
		s.fail(w, err)
		return
	}
	subnets, err := s.Store.ListSubnets(ctx, account.AccountID, region)
	if err != nil {
		s.fail(w, err)
		return
	}
	tasks := byTarget[awsTarget{account.AccountID, region}]
	out := NetworkRegionVPCsDTO{Items: []NetworkVPCCardDTO{}, PolledAt: newestPolledAt(tasks, vpcs)}
	for _, summary := range network.SummarizeVPCs(vpcs, subnets, tasks) {
		clusters := summary.Clusters
		if clusters == nil {
			clusters = []string{}
		}
		out.Items = append(out.Items, NetworkVPCCardDTO{
			VPCID: summary.VPC.VPCID, Name: summary.VPC.Name, CIDR: summary.VPC.CIDR,
			Subnets: summary.Subnets, Clusters: clusters, Tasks: summary.Tasks,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// getVPCTree answers GET /api/vpcs/{vpcId}/tree?account=&region=; a VPC with nothing visible is 404.
func (s *Server) getVPCTree(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	accountID, region := sc.Filter.AccountID, sc.Filter.Region
	if accountID == "" || region == "" {
		s.fail(w, invalid("account and region are required."))
		return
	}
	account, err := s.findAccountRegion(ctx, accountID, region)
	if err != nil {
		s.fail(w, err)
		return
	}
	vpc, err := s.Store.GetVPC(ctx, accountID, region, r.PathValue("vpcId"))
	if err != nil {
		s.fail(w, err)
		return
	}
	subnets, err := s.Store.ListSubnets(ctx, accountID, region)
	if err != nil {
		s.fail(w, err)
		return
	}
	view, err := s.loadEstate(ctx, sc.Filter, sc.Vis, "", nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	tasks := visibleTasks(sc, view.runningTasks)
	if network.ActiveVPCs(subnets, tasks)[vpc.VPCID] == 0 {
		s.fail(w, store.ErrNotFound)
		return
	}
	edges, err := s.Store.ListVPCEdges(ctx, accountID, region, vpc.VPCID)
	if err != nil {
		s.fail(w, err)
		return
	}
	clusterTags, err := s.networkClusterTags(ctx, sc.Tags, tasks)
	if err != nil {
		s.fail(w, err)
		return
	}
	services := make(map[model.ServiceKey]network.ServiceInfo, len(view.services))
	for _, svc := range view.services {
		services[svc.Key] = network.ServiceInfo{ID: svc.Key.ID(), Status: view.statusByService[svc.Key], Revision: svc.Revision}
	}
	tree := network.Assemble(network.Input{
		VPC: vpc, Subnets: subnets, Edges: edges, Tasks: tasks,
		Services: services, ClusterTags: clusterTags, CollapseThreshold: network.DefaultCollapseThreshold,
	})
	if tree.Edges == nil {
		tree.Edges = []network.Edge{}
	}
	writeJSON(w, http.StatusOK, VPCTreeDTO{
		VPC: VPCRefHeaderDTO{
			VPCID: vpc.VPCID, Name: vpc.Name, CIDR: vpc.CIDR,
			AccountID: account.AccountID, AccountName: account.Name, Region: region,
		},
		Edges: tree.Edges, Summary: tree.Summary, Root: tree.Root,
		PolledAt: newestPolledAt(tasks, []model.VPC{vpc}),
	})
}

// networkClusterTags resolves the effective tag name and color of every cluster that has a task.
func (s *Server) networkClusterTags(ctx context.Context, tags authz.Tags, tasks []model.Task) (map[model.ClusterKey]network.TagInfo, error) {
	all, err := s.Store.ListTags(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[authz.TagID]network.TagInfo, len(all))
	for _, tag := range all {
		byID[tag.ID] = network.TagInfo{Name: tag.Name, Color: tag.Color}
	}
	out := map[model.ClusterKey]network.TagInfo{}
	for _, task := range tasks {
		if _, done := out[task.Cluster]; !done {
			out[task.Cluster] = byID[authz.EffectiveClusterTag(tags, task.Cluster)]
		}
	}
	return out, nil
}
