package fake

import (
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	astypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// This file holds the plan 03 (observe) part of the estate: metric profiles,
// target groups, auto scaling, log pools and Container Insights modes, plus
// the Estate helpers tests use to reshape them.

// MetricProfile shapes the deterministic CloudWatch series of one service.
// CPU and memory bases are percentages, network bases are KB/s.
type MetricProfile struct {
	CPUBase, MemBase, NetInBase, NetOutBase float64
	LB                                      bool
	RPMScale                                float64 // requests per minute = 50 * RPMScale
	Unhealthy                               bool
	Running                                 bool // false: every CPU and memory value is 0
}

// TargetGroup is one ELBv2 target group behind a service.
type TargetGroup struct {
	ARN, LoadBalancerARN string
	Healthy, Unhealthy   int
}

// ScalingSeed describes the Application Auto Scaling setup of a service: the
// scalable target's range plus its policies and scheduled actions. The write
// calls of the fake AutoScalingAPI mutate it in place.
type ScalingSeed struct {
	Min, Max  int
	Policies  []PolicySeed
	Scheduled []ScheduledSeed
}

// PolicySeed is one scaling policy. Step policies carry no further settings
// (the fake only keeps their name and type); target tracking policies use a
// predefined metric.
type PolicySeed struct {
	Name              string
	Step              bool
	Metric            astypes.MetricType
	Target            float64
	ScaleOut, ScaleIn int32
	DisableScaleIn    bool
	ResourceLabel     string // ALBRequestCountPerTarget only
}

// ScheduledSeed is one scheduled action. A nil Min or Max leaves that bound alone.
type ScheduledSeed struct {
	Name, Schedule, Timezone string
	Min, Max                 *int32
}

func cpuTargetPolicy(target float64) PolicySeed {
	return PolicySeed{Name: "cpu-target", Metric: astypes.MetricTypeECSServiceAverageCPUUtilization, Target: target,
		ScaleOut: scaleOutCooldownSeconds, ScaleIn: scaleInCooldownSeconds}
}

// LogLine is one explicit log event in a log group (see Estate.SetLogLines).
type LogLine struct {
	Stream    string
	Timestamp time.Time
	Message   string
}

// Scaling cooldowns every seeded target-tracking policy uses (seconds).
const (
	scaleOutCooldownSeconds = 60
	scaleInCooldownSeconds  = 300
)

var mockupLogPools = map[string][][2]string{
	"default": {{"info", "GET /health 200 2ms"}, {"info", "GET /api/v1/items 200 41ms"}, {"info", "POST /api/v1/items 201 88ms"},
		{"warn", "slow query took 812ms: SELECT * FROM items WHERE owner_id = $1"}, {"info", "cache hit ratio 0.93"},
		{"error", "upstream timeout after 5000ms (retrying 1/3)"}, {"info", "GET /api/v1/items/482 200 12ms"}},
	"payments-api": {{"info", "POST /v1/charges 201 143ms provider=stripe"}, {"info", "GET /health 200 1ms"},
		{"info", "webhook evt_1Q8 received type=charge.succeeded"}, {"warn", "idempotency key reused key=ik_7f3a"},
		{"info", "POST /v1/refunds 200 98ms"}, {"error", "card_declined code=insufficient_funds charge=ch_3Q8x"}},
	"image-resizer": {{"info", "starting image-resizer 0.4.2 on :8090"}, {"info", "loaded libvips 8.15.3"},
		{"info", "resizing upload 9f2e.jpg 7952x5304 -> 2048w"}, {"warn", "memory usage 486MiB of 512MiB"},
		{"error", "fatal: runtime: out of memory"}, {"error", "Container killed due to memory usage"}},
	"jenkins-controller": {{"info", "Started pipeline platform/main #1842"}, {"info", "Provisioning EKS agent pod jnlp-k8s-7c2f"},
		{"info", "Agent jnlp-k8s-7c2f is connected"}, {"info", "Finished: SUCCESS"}, {"warn", "Disk usage of JENKINS_HOME at 71%"}},
}

// Request rates of the mockup's busiest services (requests per minute "now").
var mockupRPM = map[string]float64{"payments-api": 367, "web-frontend": 731, "auth-service": 760}

var mockupScaling = map[string]ScalingSeed{
	"payments-api": {Min: 2, Max: 6, Policies: []PolicySeed{cpuTargetPolicy(60)}, Scheduled: []ScheduledSeed{
		{Name: "scheduled-1", Schedule: "cron(0 7 ? * MON-FRI *)", Min: aws.Int32(3)},
		{Name: "scheduled-2", Schedule: "cron(0 20 ? * MON-FRI *)", Min: aws.Int32(2)}}},
	"web-frontend": {Min: 2, Max: 8, Policies: []PolicySeed{cpuTargetPolicy(55)}},
	"catalog-api":  {Min: 2, Max: 4, Policies: []PolicySeed{cpuTargetPolicy(65)}},
}

func serviceKeyOf(accountID, region, cluster, service string) model.ServiceKey {
	return model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: accountID, Region: region, Cluster: cluster}, Service: service}
}

// seedObserve fills the plan 03 maps. Called at the end of MockupEstate, after
// services and tasks exist, so ARNs agree with the ECS side.
func seedObserve(e *Estate) {
	e.MetricProfiles = map[model.ServiceKey]MetricProfile{}
	e.TargetGroups = map[model.ServiceKey]TargetGroup{}
	e.ExtraTargetGroups = map[model.ServiceKey][]TargetGroup{}
	e.ScalingConfig = map[model.ServiceKey]*ScalingSeed{}
	e.ContainerInsights = map[model.ClusterKey]string{}
	e.LogLines = map[string][]LogLine{}
	e.LogPools = map[string][][2]string{}
	for name, pool := range mockupLogPools {
		e.LogPools[name] = append([][2]string(nil), pool...)
	}

	for _, cl := range e.Clusters {
		key := model.ClusterKey{AccountID: cl.AccountID, Region: cl.Region, Cluster: aws.ToString(cl.Cluster.ClusterName)}
		mode := "enabled"
		switch key.Cluster {
		case "edge-proxy":
			mode = "disabled"
		case "core-services":
			mode = "enhanced"
		}
		e.ContainerInsights[key] = mode
	}
	for i := range e.Clusters {
		cl := &e.Clusters[i]
		key := model.ClusterKey{AccountID: cl.AccountID, Region: cl.Region, Cluster: aws.ToString(cl.Cluster.ClusterName)}
		settings := append([]ecstypes.ClusterSetting(nil), cl.Cluster.Settings...)
		for j := range settings {
			if settings[j].Name == ecstypes.ClusterSettingNameContainerInsights {
				settings[j].Value = aws.String(e.ContainerInsights[key])
			}
		}
		cl.Cluster.Settings = settings
	}

	for _, sp := range seedServices {
		c := clusterSeed(sp.cluster)
		key := serviceKeyOf(c.accountID, c.region, sp.cluster, sp.name)
		rng := rand.New(rand.NewPCG(hashSeed(sp.cluster+"/"+sp.name, 7), 0x6f627365727665))
		running := sp.running > 0
		profile := MetricProfile{
			MemBase:    35 + rng.Float64()*30,
			NetInBase:  20 + rng.Float64()*20,
			NetOutBase: 12 + rng.Float64()*15,
			LB:         sp.lb,
			RPMScale:   0.6 + rng.Float64()*0.8,
			Unhealthy:  sp.unhealthy > 0,
			Running:    running,
		}
		if running {
			profile.CPUBase = 18 + rng.Float64()*30
		} else {
			profile.CPUBase = 2
		}
		if rpm, ok := mockupRPM[sp.name]; ok {
			profile.RPMScale = rpm / 50
		}
		e.MetricProfiles[key] = profile

		if cfg, ok := mockupScaling[sp.name]; ok {
			cfg.Policies = append([]PolicySeed(nil), cfg.Policies...)
			cfg.Scheduled = append([]ScheduledSeed(nil), cfg.Scheduled...)
			e.ScalingConfig[key] = &cfg
		}

		if !sp.lb || sp.port == 0 {
			continue
		}
		for _, svc := range e.Services {
			if svc.AccountID != c.accountID || svc.Region != c.region || svc.ClusterName != sp.cluster || aws.ToString(svc.Service.ServiceName) != sp.name {
				continue
			}
			if len(svc.Service.LoadBalancers) == 0 {
				continue
			}
			e.TargetGroups[key] = TargetGroup{
				ARN:             aws.ToString(svc.Service.LoadBalancers[0].TargetGroupArn),
				LoadBalancerARN: loadBalancerARN(c.region, c.accountID, sp.cluster),
				Healthy:         sp.running - sp.unhealthy,
				Unhealthy:       sp.unhealthy,
			}
		}
	}
}

func loadBalancerARN(region, account, cluster string) string {
	return "arn:aws:elasticloadbalancing:" + region + ":" + account + ":loadbalancer/app/" + cluster + "-alb/" +
		strings.ToLower(hexOf(hashSeed(cluster+"-alb", 3)))
}

func hexOf(n uint64) string {
	const digits = "0123456789abcdef"
	var b [16]byte
	for i := 15; i >= 0; i-- {
		b[i] = digits[n&0xf]
		n >>= 4
	}
	return string(b[:])
}

// SetTargetHealth sets the healthy/unhealthy target counts of the service's
// primary target group.
func (e *Estate) SetTargetHealth(key model.ServiceKey, healthy, unhealthy int) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	tg, ok := e.TargetGroups[key]
	if !ok {
		return
	}
	tg.Healthy, tg.Unhealthy = healthy, unhealthy
	e.TargetGroups[key] = tg
}

// AddTargetGroup attaches a further target group to the service: it becomes
// visible to ELB describe calls and is added to the ECS service's load
// balancers. Empty ARN fields are generated.
func (e *Estate) AddTargetGroup(key model.ServiceKey, tg TargetGroup) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	if tg.ARN == "" {
		tg.ARN = "arn:aws:elasticloadbalancing:" + key.Region + ":" + key.AccountID + ":targetgroup/" + key.Service + "-tg-" +
			itoa(len(e.ExtraTargetGroups[key])+2) + "/" + hexOf(hashSeed(key.Service, len(e.ExtraTargetGroups[key])))
	}
	if tg.LoadBalancerARN == "" {
		tg.LoadBalancerARN = loadBalancerARN(key.Region, key.AccountID, key.Cluster+"-"+itoa(len(e.ExtraTargetGroups[key])+2))
	}
	e.ExtraTargetGroups[key] = append(e.ExtraTargetGroups[key], tg)
	for i := range e.Services {
		svc := &e.Services[i]
		if svc.AccountID == key.AccountID && svc.Region == key.Region && svc.ClusterName == key.Cluster && aws.ToString(svc.Service.ServiceName) == key.Service {
			lbs := append([]ecstypes.LoadBalancer(nil), svc.Service.LoadBalancers...)
			svc.Service.LoadBalancers = append(lbs, ecstypes.LoadBalancer{
				TargetGroupArn: aws.String(tg.ARN), ContainerName: aws.String(key.Service), ContainerPort: aws.Int32(8080)})
		}
	}
}

// RemoveTargetGroup detaches the target group from its service and forgets
// it, so describing it afterwards fails with TargetGroupNotFound.
func (e *Estate) RemoveTargetGroup(arn string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	for key, tg := range e.TargetGroups {
		if tg.ARN == arn {
			delete(e.TargetGroups, key)
		}
	}
	for key, groups := range e.ExtraTargetGroups {
		var kept []TargetGroup
		for _, tg := range groups {
			if tg.ARN != arn {
				kept = append(kept, tg)
			}
		}
		e.ExtraTargetGroups[key] = kept
	}
	for i := range e.Services {
		svc := &e.Services[i]
		var kept []ecstypes.LoadBalancer
		for _, lb := range svc.Service.LoadBalancers {
			if aws.ToString(lb.TargetGroupArn) != arn {
				kept = append(kept, lb)
			}
		}
		if len(kept) != len(svc.Service.LoadBalancers) {
			svc.Service.LoadBalancers = kept
		}
	}
}

// SetLogLines makes the log group serve exactly these events (overriding the
// generated pool). Passing no lines restores the generated behaviour.
func (e *Estate) SetLogLines(group string, lines []LogLine) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	if e.LogLines == nil {
		e.LogLines = map[string][]LogLine{}
	}
	if len(lines) == 0 {
		delete(e.LogLines, group)
		return
	}
	e.LogLines[group] = append([]LogLine(nil), lines...)
}

// SetLogGroupStoredBytes makes DescribeLogGroups report this storedBytes for the group, and makes the
// group exist even when it has no lines.
func (e *Estate) SetLogGroupStoredBytes(group string, bytes int64) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	if e.LogGroupBytes == nil {
		e.LogGroupBytes = map[string]int64{}
	}
	e.LogGroupBytes[group] = bytes
}

type targetGroupEntry struct {
	key   model.ServiceKey
	group TargetGroup
}

// allTargetGroups lists primary and extra groups in a stable order. Caller holds Mu.
func (e *Estate) allTargetGroups() []targetGroupEntry {
	var out []targetGroupEntry
	for key, tg := range e.TargetGroups {
		out = append(out, targetGroupEntry{key, tg})
		for _, extra := range e.ExtraTargetGroups[key] {
			out = append(out, targetGroupEntry{key, extra})
		}
	}
	for key, extras := range e.ExtraTargetGroups {
		if _, hasPrimary := e.TargetGroups[key]; hasPrimary {
			continue
		}
		for _, extra := range extras {
			out = append(out, targetGroupEntry{key, extra})
		}
	}
	slices.SortFunc(out, func(a, b targetGroupEntry) int { return strings.Compare(a.group.ARN, b.group.ARN) })
	return out
}

// runningAndDesired counts the service's RUNNING tasks and desired count. Caller holds Mu.
func (e *Estate) runningAndDesired(key model.ServiceKey) (running, desired int) {
	for _, s := range e.Services {
		if s.AccountID == key.AccountID && s.Region == key.Region && s.ClusterName == key.Cluster && aws.ToString(s.Service.ServiceName) == key.Service {
			running, desired = int(s.Service.RunningCount), int(s.Service.DesiredCount)
		}
	}
	return running, desired
}

// clusterCounts sums running and desired tasks over the cluster's services. Caller holds Mu.
func (e *Estate) clusterCounts(key model.ClusterKey) (running, desired int) {
	for _, s := range e.Services {
		if s.AccountID == key.AccountID && s.Region == key.Region && s.ClusterName == key.Cluster {
			running += int(s.Service.RunningCount)
			desired += int(s.Service.DesiredCount)
		}
	}
	return running, desired
}

// taskPrivateIP reads the task's ENI private IPv4 address.
func taskPrivateIP(attachments []ecstypes.Attachment) string {
	for _, a := range attachments {
		for _, d := range a.Details {
			if aws.ToString(d.Name) == "privateIPv4Address" {
				return aws.ToString(d.Value)
			}
		}
	}
	return ""
}

func sortServiceKeys(keys []model.ServiceKey) {
	slices.SortFunc(keys, func(a, b model.ServiceKey) int { return strings.Compare(a.ID(), b.ID()) })
}
