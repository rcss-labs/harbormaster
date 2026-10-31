package poller

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// chunk splits xs into consecutive slices of at most n elements.
func chunk[T any](xs []T, n int) [][]T {
	if n <= 0 {
		n = 1
	}
	var out [][]T
	for len(xs) > n {
		out = append(out, xs[:n])
		xs = xs[n:]
	}
	if len(xs) > 0 {
		out = append(out, xs)
	}
	return out
}

// rawJSON stores the SDK struct as-is; plans 02/04 unmarshal it back into ecstypes.*.
func rawJSON(v any) json.RawMessage {
	encoded, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return encoded
}

// utcTime converts to UTC so content hashes do not depend on the process time zone.
func utcTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

func utcTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// parseTaskDefinitionARN splits "...:task-definition/<family>:<revision>".
func parseTaskDefinitionARN(arn string) (family string, revision int) {
	segment := arn[strings.LastIndex(arn, "/")+1:]
	if i := strings.LastIndex(segment, ":"); i >= 0 {
		revision, _ = strconv.Atoi(segment[i+1:])
		return segment[:i], revision
	}
	return segment, 0
}

func clusterNameFromARN(arn string) string { return arn[strings.LastIndex(arn, "/")+1:] }

func normalizeCluster(t awsx.Target, c ecstypes.Cluster, now time.Time) model.Cluster {
	insights := false
	for _, setting := range c.Settings {
		if setting.Name == ecstypes.ClusterSettingNameContainerInsights {
			value := aws.ToString(setting.Value)
			insights = value == "enabled" || value == "enhanced"
		}
	}
	return model.Cluster{
		Key:               model.ClusterKey{AccountID: t.AccountID, Region: t.Region, Cluster: aws.ToString(c.ClusterName)},
		ARN:               aws.ToString(c.ClusterArn),
		Status:            aws.ToString(c.Status),
		RunningTasks:      int(c.RunningTasksCount),
		PendingTasks:      int(c.PendingTasksCount),
		ActiveServices:    int(c.ActiveServicesCount),
		ContainerInsights: insights,
		CapacityProviders: c.CapacityProviders,
		Raw:               rawJSON(c),
		PolledAt:          now.UTC(),
	}
}

func normalizeService(t awsx.Target, cluster string, s ecstypes.Service, now time.Time) (model.Service, []model.ServiceEvent) {
	key := model.ServiceKey{
		ClusterKey: model.ClusterKey{AccountID: t.AccountID, Region: t.Region, Cluster: cluster},
		Service:    aws.ToString(s.ServiceName),
	}
	out := model.Service{
		Key:                  key,
		ARN:                  aws.ToString(s.ServiceArn),
		Status:               aws.ToString(s.Status),
		Desired:              int(s.DesiredCount),
		Running:              int(s.RunningCount),
		Pending:              int(s.PendingCount),
		TaskDefinitionARN:    aws.ToString(s.TaskDefinition),
		LaunchType:           string(s.LaunchType),
		PlatformVersion:      aws.ToString(s.PlatformVersion),
		SchedulingStrategy:   string(s.SchedulingStrategy),
		EnableExecuteCommand: s.EnableExecuteCommand,
		CreatedAt:            utcTime(s.CreatedAt),
		Raw:                  rawJSON(s),
		PolledAt:             now.UTC(),
	}
	if s.DeploymentController != nil {
		out.DeploymentController = string(s.DeploymentController.Type)
	}
	if cfg := s.DeploymentConfiguration; cfg != nil {
		out.MinHealthyPercent = int(aws.ToInt32(cfg.MinimumHealthyPercent))
		out.MaxPercent = int(aws.ToInt32(cfg.MaximumPercent))
		if cfg.DeploymentCircuitBreaker != nil {
			out.CircuitBreakerEnable = cfg.DeploymentCircuitBreaker.Enable
			out.CircuitBreakerRollback = cfg.DeploymentCircuitBreaker.Rollback
		}
	}
	if vpc := networkOf(s.NetworkConfiguration); vpc != nil {
		out.Subnets = vpc.Subnets
		out.SecurityGroups = vpc.SecurityGroups
		out.AssignPublicIP = vpc.AssignPublicIp == ecstypes.AssignPublicIpEnabled
	}
	for _, lb := range s.LoadBalancers {
		out.LoadBalancers = append(out.LoadBalancers, model.ServiceLoadBalancer{
			TargetGroupARN: aws.ToString(lb.TargetGroupArn),
			ContainerName:  aws.ToString(lb.ContainerName),
			ContainerPort:  int(aws.ToInt32(lb.ContainerPort)),
		})
	}
	primaryARN := out.TaskDefinitionARN
	for _, d := range s.Deployments {
		deployment := model.Deployment{
			ID:                 aws.ToString(d.Id),
			Status:             aws.ToString(d.Status),
			TaskDefinitionARN:  aws.ToString(d.TaskDefinition),
			Desired:            int(d.DesiredCount),
			Running:            int(d.RunningCount),
			Pending:            int(d.PendingCount),
			Failed:             int(d.FailedTasks),
			RolloutState:       string(d.RolloutState),
			RolloutStateReason: aws.ToString(d.RolloutStateReason),
			CreatedAt:          utcTime(d.CreatedAt),
			UpdatedAt:          utcTime(d.UpdatedAt),
		}
		_, deployment.Revision = parseTaskDefinitionARN(deployment.TaskDefinitionARN)
		if deployment.Status == "PRIMARY" {
			primaryARN = deployment.TaskDefinitionARN
		}
		out.Deployments = append(out.Deployments, deployment)
	}
	if primaryARN != "" {
		out.TaskDefinitionARN = primaryARN
		out.Family, out.Revision = parseTaskDefinitionARN(primaryARN)
	}

	var events []model.ServiceEvent
	for _, e := range s.Events {
		events = append(events, model.ServiceEvent{
			Key: key, ID: aws.ToString(e.Id), CreatedAt: utcTime(e.CreatedAt), Message: aws.ToString(e.Message),
		})
	}
	return out, events
}

func networkOf(n *ecstypes.NetworkConfiguration) *ecstypes.AwsVpcConfiguration {
	if n == nil {
		return nil
	}
	return n.AwsvpcConfiguration
}

func normalizeTask(t awsx.Target, cluster string, task ecstypes.Task, now time.Time) model.Task {
	group := aws.ToString(task.Group)
	out := model.Task{
		Cluster:           model.ClusterKey{AccountID: t.AccountID, Region: t.Region, Cluster: cluster},
		ARN:               aws.ToString(task.TaskArn),
		Group:             group,
		StartedBy:         aws.ToString(task.StartedBy),
		TaskDefinitionARN: aws.ToString(task.TaskDefinitionArn),
		LastStatus:        aws.ToString(task.LastStatus),
		DesiredStatus:     aws.ToString(task.DesiredStatus),
		HealthStatus:      string(task.HealthStatus),
		LaunchType:        string(task.LaunchType),
		CapacityProvider:  aws.ToString(task.CapacityProviderName),
		CPU:               aws.ToString(task.Cpu),
		Memory:            aws.ToString(task.Memory),
		AvailabilityZone:  aws.ToString(task.AvailabilityZone),
		CreatedAt:         utcTime(task.CreatedAt),
		StartedAt:         utcTimePtr(task.StartedAt),
		StoppingAt:        utcTimePtr(task.StoppingAt),
		StoppedAt:         utcTimePtr(task.StoppedAt),
		StopCode:          string(task.StopCode),
		StoppedReason:     aws.ToString(task.StoppedReason),
		Raw:               rawJSON(task),
		PolledAt:          now.UTC(),
	}
	if name, ok := strings.CutPrefix(group, "service:"); ok {
		out.ServiceName = name
	}
	if out.LaunchType == "" && (out.CapacityProvider == "FARGATE" || out.CapacityProvider == "FARGATE_SPOT") {
		out.LaunchType = out.CapacityProvider
	}
	out.Family, out.Revision = parseTaskDefinitionARN(out.TaskDefinitionARN)

	for _, attachment := range task.Attachments {
		if aws.ToString(attachment.Type) != "ElasticNetworkInterface" {
			continue
		}
		for _, detail := range attachment.Details {
			switch aws.ToString(detail.Name) {
			case "subnetId":
				out.SubnetID = aws.ToString(detail.Value)
			case "privateIPv4Address":
				out.PrivateIP = aws.ToString(detail.Value)
			case "publicIPv4Address":
				out.PublicIP = aws.ToString(detail.Value)
			case "networkInterfaceId":
				out.ENIID = aws.ToString(detail.Value)
			}
		}
		break
	}

	for _, c := range task.Containers {
		container := model.Container{
			Name:         aws.ToString(c.Name),
			Image:        aws.ToString(c.Image),
			ImageDigest:  aws.ToString(c.ImageDigest),
			LastStatus:   aws.ToString(c.LastStatus),
			HealthStatus: string(c.HealthStatus),
			Reason:       aws.ToString(c.Reason),
			RuntimeID:    aws.ToString(c.RuntimeId),
		}
		if c.ExitCode != nil {
			code := int(*c.ExitCode)
			container.ExitCode = &code
		}
		for _, agent := range c.ManagedAgents {
			if agent.Name == ecstypes.ManagedAgentNameExecuteCommandAgent {
				container.ExecAgentStatus = aws.ToString(agent.LastStatus)
			}
		}
		out.Containers = append(out.Containers, container)
	}
	return out
}

func normalizeTaskDefinition(t awsx.Target, td ecstypes.TaskDefinition, now time.Time) model.TaskDefinitionRef {
	out := model.TaskDefinitionRef{
		AccountID:        t.AccountID,
		Region:           t.Region,
		ARN:              aws.ToString(td.TaskDefinitionArn),
		Family:           aws.ToString(td.Family),
		Revision:         int(td.Revision),
		CPU:              aws.ToString(td.Cpu),
		Memory:           aws.ToString(td.Memory),
		NetworkMode:      string(td.NetworkMode),
		TaskRoleARN:      aws.ToString(td.TaskRoleArn),
		ExecutionRoleARN: aws.ToString(td.ExecutionRoleArn),
		RegisteredAt:     utcTime(td.RegisteredAt),
		Raw:              rawJSON(td),
		PolledAt:         now.UTC(),
	}
	for _, c := range td.RequiresCompatibilities {
		out.RequiresCompatibilities = append(out.RequiresCompatibilities, string(c))
	}
	for _, cd := range td.ContainerDefinitions {
		def := model.ContainerDef{
			Name:      aws.ToString(cd.Name),
			Image:     aws.ToString(cd.Image),
			Essential: aws.ToBool(cd.Essential),
		}
		if lc := cd.LogConfiguration; lc != nil {
			def.LogDriver = string(lc.LogDriver)
			def.LogGroup = lc.Options["awslogs-group"]
			def.LogRegion = lc.Options["awslogs-region"]
			def.LogStreamPrefix = lc.Options["awslogs-stream-prefix"]
		}
		for _, m := range cd.MountPoints {
			def.MountPoints = append(def.MountPoints, model.MountPoint{
				SourceVolume: aws.ToString(m.SourceVolume), ContainerPath: aws.ToString(m.ContainerPath), ReadOnly: aws.ToBool(m.ReadOnly),
			})
		}
		out.Containers = append(out.Containers, def)
	}
	for _, v := range td.Volumes {
		if v.EfsVolumeConfiguration == nil {
			continue
		}
		volume := model.EFSVolume{Name: aws.ToString(v.Name), FileSystemID: aws.ToString(v.EfsVolumeConfiguration.FileSystemId)}
		if auth := v.EfsVolumeConfiguration.AuthorizationConfig; auth != nil {
			volume.AccessPointID = aws.ToString(auth.AccessPointId)
		}
		out.EFSVolumes = append(out.EFSVolumes, volume)
	}
	return out
}
