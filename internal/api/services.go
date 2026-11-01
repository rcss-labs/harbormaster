package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/status"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	serviceRecentEvents = 6
	deploymentHistory   = 14 * 24 * time.Hour
)

var errNotFound = &APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Not found."}

// serviceKeyFrom parses {serviceId}; a malformed id is a 404 like an invisible one.
func serviceKeyFrom(r *http.Request) (model.ServiceKey, error) {
	key, err := model.ParseServiceID(r.PathValue("serviceId"))
	if err != nil {
		return model.ServiceKey{}, errNotFound
	}
	return key, nil
}

func healthOf(t model.Task) string {
	if t.HealthStatus == "" {
		return "UNKNOWN"
	}
	return t.HealthStatus
}

// slotDTO is the running-task square of a service.
func slotDTO(svc model.Service, t model.Task) SlotDTO {
	return SlotDTO{
		TaskID: model.TaskID(t.Cluster.AccountID, t.Cluster.Region, t.ARN), ShortID: t.ShortID(),
		PrivateIP: t.PrivateIP, Health: healthOf(t), RollingOut: isRollingOut(svc, t),
	}
}

// execAgentRunning reports whether any container of t has a running ECS Exec agent.
func execAgentRunning(t model.Task) bool {
	for _, c := range t.Containers {
		if c.ExecAgentStatus == "RUNNING" {
			return true
		}
	}
	return false
}

// serviceTasks returns the service's non-STOPPED tasks with containers.
func (s *Server) serviceTasks(ctx context.Context, key model.ServiceKey, vis store.Visibility) ([]store.TaskRow, error) {
	rows, _, err := s.Store.ListTasks(ctx, store.TaskQuery{
		TargetFilter: store.TargetFilter{AccountID: key.AccountID, Region: key.Region},
		Vis:          vis, Cluster: key.Cluster, Service: key.Service, Filter: "running",
	})
	return rows, err
}

func modelTasks(rows []store.TaskRow) []model.Task {
	tasks := make([]model.Task, 0, len(rows))
	for _, r := range rows {
		tasks = append(tasks, r.Task)
	}
	return tasks
}

// targetGroupName extracts <name> from arn:...:targetgroup/<name>/<id>.
func targetGroupName(arn string) string {
	_, after, ok := strings.Cut(arn, "targetgroup/")
	if !ok {
		return arn
	}
	name, _, _ := strings.Cut(after, "/")
	return name
}

// getService answers GET /api/services/{serviceId}; invisible, unknown and malformed ids are 404.
func (s *Server) getService(w http.ResponseWriter, r *http.Request) {
	key, err := serviceKeyFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	row, err := s.Store.GetServiceView(ctx, key, sc.Vis)
	if err != nil {
		s.fail(w, err)
		return
	}
	dto, err := s.serviceDTO(ctx, sc, row)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) serviceDTO(ctx context.Context, sc scope, row store.ServiceRow) (ServiceDTO, error) {
	key := row.Key
	filter := store.TargetFilter{AccountID: key.AccountID, Region: key.Region}
	now := s.now()

	tasks, err := s.serviceTasks(ctx, key, sc.Vis)
	if err != nil {
		return ServiceDTO{}, err
	}
	unhealthy, err := s.Store.UnhealthyTargets(ctx, filter)
	if err != nil {
		return ServiceDTO{}, err
	}
	tags, err := s.tagDTOs(ctx)
	if err != nil {
		return ServiceDTO{}, err
	}
	names, err := s.accountNames(ctx)
	if err != nil {
		return ServiceDTO{}, err
	}
	td, err := s.Store.GetTaskDefinition(ctx, key.AccountID, key.Region, row.TaskDefinitionARN)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return ServiceDTO{}, err
	}
	history, err := s.Store.CountsHistory(ctx, filter, key.Cluster, key.Service, sc.Vis, now.Add(-historyWindow))
	if err != nil {
		return ServiceDTO{}, err
	}
	records, err := s.Store.ListDeployments(ctx, key, now.Add(-deploymentHistory))
	if err != nil {
		return ServiceDTO{}, err
	}
	operations, err := s.Store.ListOperations(ctx, key, true, nil, serviceRecentEvents)
	if err != nil {
		return ServiceDTO{}, err
	}
	polledAt, err := s.Store.PolledAt(ctx, filter)
	if err != nil {
		return ServiceDTO{}, err
	}

	serviceStatus := status.Service(status.Inputs{
		Service: row.Service, RunningTasks: modelTasks(tasks), TargetUnhealthy: unhealthy[key],
	})
	ref := ServiceRef{ID: key.ID(), Name: key.Service, Status: serviceStatus}
	dto := ServiceDTO{
		ID: key.ID(), Name: key.Service, Cluster: ClusterRef{ID: key.ClusterKey.ID(), Name: key.Cluster},
		AccountID: key.AccountID, AccountName: names[key.AccountID], Region: key.Region,
		Tag:    inheritedTag(tags, row.EffectiveTagID, row.TagInherited),
		Status: serviceStatus,
		Family: row.Family, Revision: row.Revision, ImageTag: td.ImageTag(key.Service),
		TaskDefinitionARN: row.TaskDefinitionARN, LaunchType: row.LaunchType,
		PlatformVersion: row.PlatformVersion, DeploymentController: row.DeploymentController,
		Desired: row.Desired, Running: row.Running, Pending: row.Pending,
		EnableExecuteCommand: row.EnableExecuteCommand,
		MinHealthyPercent:    row.MinHealthyPercent, MaxPercent: row.MaxPercent,
		CircuitBreaker:   CircuitBreakerDTO{Enable: row.CircuitBreakerEnable, Rollback: row.CircuitBreakerRollback},
		Deployments:      deploymentDTOs(row.Deployments),
		RunningTasks:     []RunningTaskDTO{},
		Slots:            []SlotDTO{},
		CountsHistory72h: pointsOf(history),
		Deployments14d:   make([]DeploymentRecordDTO, 0, len(records)),
		RecentEvents:     make([]EventDTO, 0, len(operations)),
		Can:              authz.PermissionsOf(sc.Subject, row.EffectiveTagID),
		ConsoleURL: "https://" + key.Region + ".console.aws.amazon.com/ecs/v2/clusters/" +
			key.Cluster + "/services/" + key.Service,
		PolledAt: polledAt,
	}

	parsed, err := deploy.ParseService(row.Raw)
	if err != nil {
		return ServiceDTO{}, err
	}
	dto.Controller = controllerDTO(deploy.Controller(parsed))
	dto.StrategyText = deploy.StrategyText(parsed)

	subnetTasks := map[string]int{}
	for _, t := range tasks {
		dto.RunningTasks = append(dto.RunningTasks, RunningTaskDTO{
			ID: model.TaskID(key.AccountID, key.Region, t.ARN), ShortID: t.ShortID(), Health: healthOf(t.Task),
			AZ: t.AvailabilityZone, SubnetID: t.SubnetID, PrivateIP: t.PrivateIP, Revision: t.Revision,
			Spot: t.CapacityProvider == "FARGATE_SPOT", ExecAgentRunning: execAgentRunning(t.Task),
		})
		dto.Slots = append(dto.Slots, slotDTO(row.Service, t.Task))
		if t.SubnetID != "" {
			subnetTasks[t.SubnetID]++
		}
	}
	dto.Networking = networkingDTO(row.Service, subnetTasks)
	dto.TaskDefinition = taskDefinitionDTO(row.Service, td)
	for _, rec := range records {
		dto.Deployments14d = append(dto.Deployments14d, DeploymentRecordDTO{
			DeploymentID: rec.DeploymentID, Revision: rec.Revision, Kind: rec.Kind, Outcome: rec.Outcome,
			StartedAt: rec.StartedAt, FinishedAt: rec.FinishedAt,
		})
	}
	for _, op := range operations {
		dto.RecentEvents = append(dto.RecentEvents, operationEvent(ref, op))
	}
	return dto, nil
}

// deploymentDTOs lists deployments PRIMARY first, then newest first.
func deploymentDTOs(deployments []model.Deployment) []DeploymentDTO {
	sorted := append([]model.Deployment(nil), deployments...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if (a.Status == "PRIMARY") != (b.Status == "PRIMARY") {
			return a.Status == "PRIMARY"
		}
		return a.CreatedAt.After(b.CreatedAt)
	})
	out := make([]DeploymentDTO, 0, len(sorted))
	for _, d := range sorted {
		out = append(out, DeploymentDTO{
			ID: d.ID, Status: d.Status, Revision: d.Revision, DesiredCount: d.Desired, RunningCount: d.Running,
			PendingCount: d.Pending, FailedTasks: d.Failed, RolloutState: d.RolloutState,
			RolloutStateReason: d.RolloutStateReason, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		})
	}
	return out
}

// networkingDTO lists every configured subnet, plus any subnet only tasks use, with its running task count.
func networkingDTO(svc model.Service, subnetTasks map[string]int) NetworkingDTO {
	out := NetworkingDTO{
		Subnets:        []SubnetUseDTO{},
		SecurityGroups: append([]string{}, svc.SecurityGroups...),
		AssignPublicIP: svc.AssignPublicIP,
		LoadBalancers:  []LoadBalancerDTO{},
	}
	listed := map[string]bool{}
	for _, id := range svc.Subnets {
		if !listed[id] {
			listed[id] = true
			out.Subnets = append(out.Subnets, SubnetUseDTO{ID: id, Tasks: subnetTasks[id]})
		}
	}
	var extra []string
	for id := range subnetTasks {
		if !listed[id] {
			extra = append(extra, id)
		}
	}
	sort.Strings(extra)
	for _, id := range extra {
		out.Subnets = append(out.Subnets, SubnetUseDTO{ID: id, Tasks: subnetTasks[id]})
	}
	for _, lb := range svc.LoadBalancers {
		out.LoadBalancers = append(out.LoadBalancers, LoadBalancerDTO{
			TargetGroupName: targetGroupName(lb.TargetGroupARN), ContainerName: lb.ContainerName, ContainerPort: lb.ContainerPort,
		})
	}
	return out
}

// taskDefinitionDTO maps the stored definition; when it is unknown, family and revision come from the service.
func taskDefinitionDTO(svc model.Service, td model.TaskDefinitionRef) TaskDefinitionDTO {
	out := TaskDefinitionDTO{
		Family: svc.Family, Revision: svc.Revision, CPU: td.CPU, Memory: td.Memory,
		Containers: []ContainerDefDTO{}, TaskRoleARN: td.TaskRoleARN, EFSMounts: []EFSMountDTO{},
	}
	if td.ARN != "" {
		out.Family, out.Revision = td.Family, td.Revision
	}
	for _, c := range td.Containers {
		out.Containers = append(out.Containers, ContainerDefDTO{Name: c.Name, Image: c.Image, Essential: c.Essential})
	}
	for _, vol := range td.EFSVolumes {
		for _, c := range td.Containers {
			for _, mp := range c.MountPoints {
				if mp.SourceVolume == vol.Name {
					out.EFSMounts = append(out.EFSMounts, EFSMountDTO{
						FileSystemID: vol.FileSystemID, AccessPointID: vol.AccessPointID, ContainerPath: mp.ContainerPath,
					})
				}
			}
		}
	}
	return out
}
