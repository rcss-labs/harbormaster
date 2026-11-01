package fake

import (
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

const (
	homelabAccountID = "482109375512"
	stagingAccountID = "731022849906"
)

type seedSubnet struct {
	id, cidr, az string
	public       bool
}

type seedCluster struct {
	name, accountID, region string
	insights                bool
	subnets                 []seedSubnet
}

type seedEFS struct{ name, fsID, accessPointID, path string }

var (
	efsJenkins = seedEFS{"jenkins-home", "fs-0c91a4e7", "fsap-0a1b2c3d4e", "/var/jenkins_home"}
	efsReports = seedEFS{"reports-data", "fs-02e7d1c9", "fsap-07f3e9a1c2", "/data/reports"}
	efsCerts   = seedEFS{"shared-certs", "fs-0a6b33f0", "fsap-0e44d18b7a", "/certs"}
	efsNginx   = seedEFS{"shared-certs", "fs-0a6b33f0", "fsap-0e44d18b7a", "/etc/nginx/certs"}
	efsSearch  = seedEFS{"search-index", "fs-0d18c2ab", "fsap-03c7a9e5d1", "/index"}
)

var mainVPCSubnets = []seedSubnet{
	{"subnet-03f1a9c2", "10.20.1.0/24", "us-east-1a", false},
	{"subnet-0b77e410", "10.20.2.0/24", "us-east-1b", false},
	{"subnet-09c2d5ee", "10.20.101.0/24", "us-east-1a", true},
}

var seedClusters = []seedCluster{
	{"core-services", homelabAccountID, "us-east-1", true, mainVPCSubnets},
	{"background-jobs", homelabAccountID, "us-east-1", true, mainVPCSubnets},
	{"ci-tools", homelabAccountID, "us-east-1", true, []seedSubnet{
		{"subnet-0e1c88a1", "10.21.1.0/24", "us-east-1a", false},
		{"subnet-02aa71f3", "10.21.3.0/24", "us-east-1c", false},
	}},
	{"eu-apps", homelabAccountID, "eu-west-1", true, []seedSubnet{
		{"subnet-0a90f7c1", "10.30.1.0/24", "eu-west-1a", false},
		{"subnet-0c6b1e92", "10.30.2.0/24", "eu-west-1b", false},
	}},
	{"edge-proxy", homelabAccountID, "eu-central-1", false, []seedSubnet{
		{"subnet-0d2e5a77", "10.40.1.0/24", "eu-central-1a", false},
		{"subnet-0f61c3b8", "10.40.101.0/24", "eu-central-1a", true},
	}},
	{"staging-web", stagingAccountID, "us-east-1", true, []seedSubnet{
		{"subnet-01f8a2d4", "10.50.1.0/24", "us-east-1a", false},
		{"subnet-0e7c3b19", "10.50.2.0/24", "us-east-1b", false},
	}},
}

type envVar struct{ name, value string }

// serviceSpec mirrors one SERVICE_SRC row of the mockup. A spec with
// standalone set describes a task family run outside any service.
type serviceSpec struct {
	cluster, name         string
	desired, running, rev int
	repo, tag             string
	cpu, memory, port     int
	lb, exec, sidecar     bool
	public, standalone    bool
	efs                   *seedEFS
	env                   []envVar
	deployingTag          string // a newer revision is rolling out
	oom                   bool   // tasks are OOM-killed (image-resizer)
	unhealthy             int
}

var seedServices = []serviceSpec{
	{cluster: "core-services", name: "payments-api", desired: 3, running: 3, rev: 42, repo: "payments-api", tag: "1.18.2", cpu: 512, memory: 1024, port: 8080, lb: true, exec: true, sidecar: true,
		env: []envVar{{"NODE_ENV", "production"}, {"PAYMENTS_PROVIDER", "stripe"}, {"DB_POOL_SIZE", "20"}}},
	{cluster: "core-services", name: "web-frontend", desired: 2, running: 2, rev: 87, repo: "web-frontend", tag: "3.4.0", cpu: 256, memory: 512, port: 3000, lb: true, exec: true,
		env: []envVar{{"API_URL", "http://payments-api.core.local:8080"}}},
	{cluster: "core-services", name: "auth-service", desired: 2, running: 2, rev: 15, repo: "auth-service", tag: "0.9.7", cpu: 256, memory: 512, port: 9000, lb: true, exec: true,
		env: []envVar{{"TOKEN_TTL", "3600"}, {"ISSUER", "https://auth.rcss.local"}}},
	// notifications is mid-rollout: one task on :10 (PRIMARY), one on :9 (ACTIVE).
	{cluster: "core-services", name: "notifications", desired: 2, running: 2, rev: 9, repo: "notifications", tag: "2.2.0", cpu: 256, memory: 512, port: 8081, exec: true, deployingTag: "2.3.0",
		env: []envVar{{"QUEUE_URL", "https://sqs.us-east-1.amazonaws.com/482109375512/notify"}}},
	{cluster: "background-jobs", name: "report-worker", desired: 1, running: 1, rev: 31, repo: "report-worker", tag: "5.0.1", cpu: 1024, memory: 2048, exec: true, efs: &efsReports,
		env: []envVar{{"REPORT_BUCKET", "rcss-reports"}}},
	{cluster: "background-jobs", name: "email-sender", desired: 1, running: 1, rev: 12, repo: "email-sender", tag: "1.3.3", cpu: 256, memory: 512, exec: true,
		env: []envVar{{"SMTP_HOST", "email-smtp.us-east-1.amazonaws.com"}}},
	// 256/512 is a valid Fargate size (the mockup's 512/512 is not).
	{cluster: "background-jobs", name: "image-resizer", desired: 2, running: 0, rev: 6, repo: "image-resizer", tag: "0.4.2", cpu: 256, memory: 512, port: 8090, oom: true,
		env: []envVar{{"MAX_IMAGE_MP", "40"}}},
	{cluster: "ci-tools", name: "jenkins-controller", desired: 1, running: 1, rev: 58, repo: "jenkins", tag: "2.479.1-lts", cpu: 2048, memory: 4096, port: 8080, lb: true, exec: true, efs: &efsJenkins,
		env: []envVar{{"JAVA_OPTS", "-Xmx3g"}}},
	{cluster: "ci-tools", name: "artifact-proxy", desired: 1, running: 1, rev: 7, repo: "artifact-proxy", tag: "1.0.4", cpu: 512, memory: 1024, port: 8081, exec: true,
		env: []envVar{{"UPSTREAM", "https://registry.npmjs.org"}}},
	{cluster: "eu-apps", name: "catalog-api", desired: 2, running: 2, rev: 23, repo: "catalog-api", tag: "4.1.0", cpu: 512, memory: 1024, port: 8080, lb: true, exec: true, unhealthy: 1,
		env: []envVar{{"CACHE_TTL", "300"}}},
	{cluster: "eu-apps", name: "search-indexer", desired: 1, running: 1, rev: 14, repo: "search-indexer", tag: "2.0.3", cpu: 1024, memory: 4096, exec: true, efs: &efsSearch,
		env: []envVar{{"BATCH_SIZE", "500"}}},
	{cluster: "edge-proxy", name: "edge-nginx", desired: 2, running: 2, rev: 19, repo: "edge-nginx", tag: "1.27.2", cpu: 256, memory: 512, port: 443, lb: true, exec: true, public: true, efs: &efsNginx,
		env: []envVar{{"WORKER_PROCESSES", "auto"}}},
	{cluster: "edge-proxy", name: "cert-renewer", desired: 1, running: 1, rev: 4, repo: "cert-renewer", tag: "0.2.1", cpu: 256, memory: 512, efs: &efsCerts,
		env: []envVar{{"ACME_EMAIL", "ops@rcss.local"}}},
	{cluster: "staging-web", name: "web", desired: 1, running: 1, rev: 33, repo: "web", tag: "3.5.0-rc.1", cpu: 256, memory: 512, port: 3000, lb: true, exec: true,
		env: []envVar{{"API_URL", "http://api.staging.local:8080"}}},
	{cluster: "staging-web", name: "api", desired: 1, running: 1, rev: 21, repo: "api", tag: "1.19.0-rc.2", cpu: 512, memory: 1024, port: 8080, lb: true, exec: true,
		env: []envVar{{"NODE_ENV", "staging"}}},
}

var standaloneSpecs = []serviceSpec{
	{cluster: "ci-tools", name: "release-build", rev: 3, repo: "release-build", tag: "3.0.0", cpu: 1024, memory: 2048, standalone: true, exec: true},
	{cluster: "background-jobs", name: "db-migrate", rev: 12, repo: "db-migrate", tag: "12.0.0", cpu: 512, memory: 1024, standalone: true},
}

// seedTaskDefinitionTags are applied to every seeded task definition revision.
var seedTaskDefinitionTags = []ecstypes.Tag{{Key: aws.String("managed-by"), Value: aws.String("terraform")}}

// ecrTagsFor mirrors the mockup's ecrTags: the current tag bumped +2, +1,
// current, -1, -2 on the patch number, with the deploying tag first when a
// rollout is in flight. Duplicates are dropped, as the mockup's Set does.
func ecrTagsFor(sp serviceSpec) []string {
	tags := []string{bumpTag(sp.tag, 2), bumpTag(sp.tag, 1), sp.tag, bumpTag(sp.tag, -1), bumpTag(sp.tag, -2)}
	if sp.deployingTag != "" {
		tags = append([]string{sp.deployingTag}, tags...)
	}
	seen := map[string]bool{}
	var out []string
	for _, tag := range tags {
		if !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out
}

var tagPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(.*)$`)

// bumpTag shifts the patch number of a semver-ish tag (mockup bumpTag).
func bumpTag(tag string, delta int) string {
	m := tagPattern.FindStringSubmatch(tag)
	if m == nil {
		return tag
	}
	var patch int
	fmt.Sscanf(m[3], "%d", &patch)
	patch = max(0, patch+delta)
	return fmt.Sprintf("%s.%s.%d%s", m[1], m[2], patch, m[4])
}

func hashSeed(name string, salt int) uint64 {
	h := fnv.New64a()
	h.Write([]byte(name))
	return h.Sum64() + uint64(salt)
}

// seeder builds estate entities with a deterministic random source so tests
// are stable run to run.
type seeder struct {
	e       *Estate
	rng     *rand.Rand
	usedIPs map[string]bool
}

func newSeeder(e *Estate, seed uint64) *seeder {
	return &seeder{e: e, rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), usedIPs: map[string]bool{}}
}

func (s *seeder) hex(n int) string {
	const digits = "0123456789abcdef"
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte(digits[s.rng.IntN(16)])
	}
	return b.String()
}

func (s *seeder) digits(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte(byte('0' + s.rng.IntN(10)))
	}
	return b.String()
}

func (s *seeder) uuid() string {
	return s.hex(8) + "-" + s.hex(4) + "-" + s.hex(4) + "-" + s.hex(4) + "-" + s.hex(12)
}

func (s *seeder) indexUsedIPs() {
	for _, t := range s.e.Tasks {
		for _, a := range t.Task.Attachments {
			for _, d := range a.Details {
				if aws.ToString(d.Name) == "privateIPv4Address" {
					s.usedIPs[aws.ToString(d.Value)] = true
				}
			}
		}
	}
}

func (s *seeder) ipIn(sub seedSubnet) string {
	prefix := sub.cidr[:strings.LastIndex(sub.cidr, ".")]
	for attempt := 0; ; attempt++ {
		ip := fmt.Sprintf("%s.%d", prefix, 10+s.rng.IntN(240))
		if !s.usedIPs[ip] || attempt > 500 {
			s.usedIPs[ip] = true
			return ip
		}
	}
}

func clusterSeed(name string) seedCluster {
	for _, c := range seedClusters {
		if c.name == name {
			return c
		}
	}
	panic("unknown seed cluster " + name)
}

func (s *seeder) specFor(cluster, service string) (serviceSpec, bool) {
	for _, sp := range append(append([]serviceSpec{}, seedServices...), standaloneSpecs...) {
		if sp.cluster == cluster && sp.name == service {
			return sp, true
		}
	}
	return serviceSpec{}, false
}

func (sp serviceSpec) subnets() []seedSubnet {
	var out []seedSubnet
	for _, sub := range clusterSeed(sp.cluster).subnets {
		if sub.public == sp.public {
			out = append(out, sub)
		}
	}
	return out
}

// MockupEstate returns the estate matching the design mockup. Running counts
// follow SERVICE_SRC, except notifications which has two running tasks
// (one per revision) and image-resizer which is 256 CPU / 512 MiB.
// All times are relative to now().
func MockupEstate(now func() time.Time) *Estate {
	e := &Estate{Now: now, Events: map[string][]ecstypes.ServiceEvent{}}
	e.Accounts = []Account{
		{ID: homelabAccountID, Name: "homelab-admin", Regions: []string{"us-east-1", "eu-west-1", "eu-central-1"}},
		{ID: stagingAccountID, Name: "staging-example", Regions: []string{"us-east-1"}},
	}
	sd := newSeeder(e, 20261031)
	for _, c := range seedClusters {
		sd.addCluster(c)
	}
	for _, sp := range seedServices {
		sd.addService(sp)
	}
	sd.addStandalone()
	e.recountAll()
	seedObserve(e)
	return e
}

func (s *seeder) addCluster(c seedCluster) {
	insights := "disabled"
	if c.insights {
		insights = "enabled"
	}
	s.e.Clusters = append(s.e.Clusters, Cluster{AccountID: c.accountID, Region: c.region, Cluster: ecstypes.Cluster{
		ClusterArn:        aws.String(clusterARN(c.region, c.accountID, c.name)),
		ClusterName:       aws.String(c.name),
		Status:            aws.String("ACTIVE"),
		CapacityProviders: []string{"FARGATE", "FARGATE_SPOT"},
		Settings:          []ecstypes.ClusterSetting{{Name: ecstypes.ClusterSettingNameContainerInsights, Value: aws.String(insights)}},
	}})
}

func (s *seeder) buildTaskDefinition(sp serviceSpec, revision int, tag string, registered time.Time) TaskDefinition {
	c := clusterSeed(sp.cluster)
	logCfg := &ecstypes.LogConfiguration{LogDriver: ecstypes.LogDriverAwslogs, Options: map[string]string{
		"awslogs-group": "/ecs/" + sp.cluster + "/" + sp.name, "awslogs-region": c.region, "awslogs-stream-prefix": "ecs"}}
	main := ecstypes.ContainerDefinition{
		Name:             aws.String(sp.name),
		Image:            aws.String(c.accountID + ".dkr.ecr." + c.region + ".amazonaws.com/" + sp.repo + ":" + tag),
		Essential:        aws.Bool(true),
		LogConfiguration: logCfg,
	}
	if sp.port > 0 {
		main.PortMappings = []ecstypes.PortMapping{{ContainerPort: aws.Int32(int32(sp.port)), Protocol: ecstypes.TransportProtocolTcp}}
		main.HealthCheck = &ecstypes.HealthCheck{
			Command:  []string{"CMD-SHELL", fmt.Sprintf("wget -qO- http://localhost:%d/health || exit 1", sp.port)},
			Interval: aws.Int32(30), Timeout: aws.Int32(5), Retries: aws.Int32(3)}
	}
	for _, ev := range sp.env {
		main.Environment = append(main.Environment, ecstypes.KeyValuePair{Name: aws.String(ev.name), Value: aws.String(ev.value)})
	}
	td := ecstypes.TaskDefinition{
		Family:                  aws.String(sp.name),
		Revision:                int32(revision),
		TaskDefinitionArn:       aws.String(taskDefinitionARN(c.region, c.accountID, sp.name, revision)),
		TaskRoleArn:             aws.String("arn:aws:iam::" + c.accountID + ":role/" + sp.name + "-task"),
		ExecutionRoleArn:        aws.String("arn:aws:iam::" + c.accountID + ":role/ecs-task-execution"),
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		Compatibilities:         []ecstypes.Compatibility{ecstypes.CompatibilityEc2, ecstypes.CompatibilityFargate},
		Cpu:                     aws.String(itoa(sp.cpu)),
		Memory:                  aws.String(itoa(sp.memory)),
		Status:                  ecstypes.TaskDefinitionStatusActive,
		RegisteredAt:            aws.Time(registered),
		RegisteredBy:            aws.String("terraform (TerraDuckTel)"),
	}
	if sp.efs != nil {
		main.MountPoints = []ecstypes.MountPoint{{SourceVolume: aws.String(sp.efs.name), ContainerPath: aws.String(sp.efs.path), ReadOnly: aws.Bool(false)}}
		td.Volumes = []ecstypes.Volume{{Name: aws.String(sp.efs.name), EfsVolumeConfiguration: &ecstypes.EFSVolumeConfiguration{
			FileSystemId:        aws.String(sp.efs.fsID),
			TransitEncryption:   ecstypes.EFSTransitEncryptionEnabled,
			AuthorizationConfig: &ecstypes.EFSAuthorizationConfig{AccessPointId: aws.String(sp.efs.accessPointID), Iam: ecstypes.EFSAuthorizationConfigIAMEnabled}}}}
	}
	td.ContainerDefinitions = []ecstypes.ContainerDefinition{main}
	if sp.sidecar {
		td.ContainerDefinitions = append(td.ContainerDefinitions, ecstypes.ContainerDefinition{
			Name: aws.String("otel-collector"), Image: aws.String("public.ecr.aws/aws-observability/aws-otel-collector:v0.40.0"),
			Essential: aws.Bool(false), LogConfiguration: &ecstypes.LogConfiguration{LogDriver: ecstypes.LogDriverAwsfirelens}})
	}
	return TaskDefinition{AccountID: c.accountID, Region: c.region, Definition: td, Tags: deepCopy(seedTaskDefinitionTags)}
}

func (s *seeder) findTaskDefinition(arn string) (ecstypes.TaskDefinition, bool) {
	for _, td := range s.e.TaskDefinitions {
		if aws.ToString(td.Definition.TaskDefinitionArn) == arn {
			return td.Definition, true
		}
	}
	return ecstypes.TaskDefinition{}, false
}

type taskOptions struct {
	taskDefinitionARN string
	slot              int // picks the subnet round-robin
	startedAt         time.Time
	health            ecstypes.HealthStatus // zero: derived from the service
	stoppedAt         time.Time             // non-zero: STOPPED
	reason            string
	exitCode          int32
	stopCode          ecstypes.TaskStopCode
	deploymentID      string
	group, startedBy  string // overrides for standalone tasks
	agentPending      bool   // the ExecuteCommandAgent is PENDING instead of RUNNING (plan 04)
}

func (s *seeder) addTask(sp serviceSpec, o taskOptions) {
	c := clusterSeed(sp.cluster)
	td, _ := s.findTaskDefinition(o.taskDefinitionARN)
	subs := sp.subnets()
	sub := subs[o.slot%len(subs)]
	id := s.hex(32)
	arn := taskARN(c.region, c.accountID, sp.cluster, id)
	stopped := !o.stoppedAt.IsZero()
	status := "RUNNING"
	if stopped {
		status = "STOPPED"
	}

	health := o.health
	if health == "" {
		health = ecstypes.HealthStatusUnknown
		if sp.port > 0 && !stopped && !sp.standalone {
			health = ecstypes.HealthStatusHealthy
		}
	}
	capacity := "FARGATE"
	if s.rng.Float64() > 0.85 {
		capacity = "FARGATE_SPOT"
	}

	details := []ecstypes.KeyValuePair{
		{Name: aws.String("subnetId"), Value: aws.String(sub.id)},
		{Name: aws.String("networkInterfaceId"), Value: aws.String("eni-0" + s.hex(16))},
		{Name: aws.String("macAddress"), Value: aws.String("0a:" + s.hex(2) + ":" + s.hex(2) + ":" + s.hex(2) + ":" + s.hex(2) + ":" + s.hex(2))},
		{Name: aws.String("privateIPv4Address"), Value: aws.String(s.ipIn(sub))},
	}
	if sp.public {
		details = append(details, ecstypes.KeyValuePair{Name: aws.String("publicIPv4Address"),
			Value: aws.String(fmt.Sprintf("3.%d.%d.%d", 64+s.rng.IntN(100), s.rng.IntN(250), 1+s.rng.IntN(250)))})
	}

	var containers []ecstypes.Container
	for i, cd := range td.ContainerDefinitions {
		ct := ecstypes.Container{
			ContainerArn:      aws.String("arn:aws:ecs:" + c.region + ":" + c.accountID + ":container/" + sp.cluster + "/" + id + "/" + s.uuid()),
			TaskArn:           aws.String(arn),
			Name:              cd.Name,
			Image:             cd.Image,
			LastStatus:        aws.String(status),
			HealthStatus:      ecstypes.HealthStatusUnknown,
			NetworkInterfaces: []ecstypes.NetworkInterface{{PrivateIpv4Address: details[3].Value}},
		}
		if i == 0 {
			ct.HealthStatus = health
			if sp.exec {
				agentStatus := "RUNNING"
				if stopped {
					agentStatus = "STOPPED"
				} else if o.agentPending {
					agentStatus = "PENDING"
				}
				ct.ManagedAgents = []ecstypes.ManagedAgent{{Name: ecstypes.ManagedAgentNameExecuteCommandAgent, LastStatus: aws.String(agentStatus)}}
			}
		}
		if stopped {
			ct.ExitCode = aws.Int32(o.exitCode)
		}
		containers = append(containers, ct)
	}

	group, startedBy := o.group, o.startedBy
	if group == "" {
		group = "service:" + sp.name
	}
	if startedBy == "" {
		startedBy = o.deploymentID
	}
	task := ecstypes.Task{
		TaskArn:              aws.String(arn),
		ClusterArn:           aws.String(clusterARN(c.region, c.accountID, sp.cluster)),
		TaskDefinitionArn:    aws.String(o.taskDefinitionARN),
		LastStatus:           aws.String(status),
		DesiredStatus:        aws.String(status),
		HealthStatus:         health,
		LaunchType:           ecstypes.LaunchTypeFargate,
		CapacityProviderName: aws.String(capacity),
		Cpu:                  td.Cpu,
		Memory:               td.Memory,
		AvailabilityZone:     aws.String(sub.az),
		Group:                aws.String(group),
		StartedBy:            aws.String(startedBy),
		PlatformVersion:      aws.String("1.4.0"),
		PlatformFamily:       aws.String("Linux"),
		Connectivity:         ecstypes.ConnectivityConnected,
		Version:              3,
		CreatedAt:            aws.Time(o.startedAt.Add(-30 * time.Second)),
		StartedAt:            aws.Time(o.startedAt),
		EnableExecuteCommand: sp.exec,
		Attachments: []ecstypes.Attachment{{
			Id: aws.String(s.uuid()), Type: aws.String("ElasticNetworkInterface"), Status: aws.String("ATTACHED"), Details: details}},
		Containers: containers,
	}
	if stopped {
		task.StoppedAt = aws.Time(o.stoppedAt)
		task.StoppingAt = aws.Time(o.stoppedAt)
		task.StoppedReason = aws.String(o.reason)
		task.StopCode = o.stopCode
	}
	s.e.Tasks = append(s.e.Tasks, Task{AccountID: c.accountID, Region: c.region, ClusterName: sp.cluster, Task: task})
}

func (s *seeder) addService(sp serviceSpec) {
	c := clusterSeed(sp.cluster)
	now := s.e.now()

	// Revision -> task definition. Non-OOM services get their two previous
	// revisions (image-resizer only :6); the new revision exists only mid-rollout.
	tdAt := func(rev int, tag string, age time.Duration) string {
		td := s.buildTaskDefinition(sp, rev, tag, now.Add(-age))
		s.e.TaskDefinitions = append(s.e.TaskDefinitions, td)
		return aws.ToString(td.Definition.TaskDefinitionArn)
	}
	var currentARN, previousARN, newARN string
	if !sp.oom {
		tdAt(sp.rev-2, bumpTag(sp.tag, -2), 6*24*time.Hour)
		previousARN = tdAt(sp.rev-1, bumpTag(sp.tag, -1), 4*24*time.Hour+time.Duration(s.rng.Float64()*float64(24*time.Hour)))
	}
	currentARN = tdAt(sp.rev, sp.tag, time.Duration(s.rng.Float64()*float64(24*time.Hour)))
	if sp.deployingTag != "" {
		newARN = tdAt(sp.rev+1, sp.deployingTag, 3*time.Minute)
	}

	primaryID := "ecs-svc/" + s.digits(19)
	primaryARN, primaryAge := currentARN, 30*time.Hour
	var deployments []ecstypes.Deployment
	if newARN != "" {
		oldID := primaryID
		primaryID = "ecs-svc/" + s.digits(19)
		primaryARN, primaryAge = newARN, 3*time.Minute
		deployments = append(deployments, ecstypes.Deployment{
			Id: aws.String(oldID), Status: aws.String("ACTIVE"), TaskDefinition: aws.String(currentARN),
			DesiredCount: 1, RolloutState: ecstypes.DeploymentRolloutStateCompleted,
			RolloutStateReason: aws.String("ECS deployment " + oldID + " completed."),
			CreatedAt:          aws.Time(now.Add(-30 * time.Hour)), UpdatedAt: aws.Time(now.Add(-30 * time.Hour)),
			LaunchType: ecstypes.LaunchTypeFargate, NetworkConfiguration: serviceNetwork(sp),
		})
	}
	primary := ecstypes.Deployment{
		Id: aws.String(primaryID), Status: aws.String("PRIMARY"), TaskDefinition: aws.String(primaryARN),
		DesiredCount: int32(sp.desired), RolloutState: ecstypes.DeploymentRolloutStateCompleted,
		RolloutStateReason: aws.String("ECS deployment " + primaryID + " completed."),
		CreatedAt:          aws.Time(now.Add(-primaryAge)), UpdatedAt: aws.Time(now.Add(-primaryAge)),
		LaunchType: ecstypes.LaunchTypeFargate, NetworkConfiguration: serviceNetwork(sp),
	}
	if sp.deployingTag != "" {
		primary.RolloutState = ecstypes.DeploymentRolloutStateInProgress
		primary.RolloutStateReason = aws.String("ECS deployment " + primaryID + " in progress.")
	}
	if sp.oom {
		primary.FailedTasks = 6
	}
	deployments = append([]ecstypes.Deployment{primary}, deployments...)

	svcARN := serviceARN(c.region, c.accountID, sp.cluster, sp.name)
	svc := ecstypes.Service{
		ServiceArn:      aws.String(svcARN),
		ServiceName:     aws.String(sp.name),
		ClusterArn:      aws.String(clusterARN(c.region, c.accountID, sp.cluster)),
		Status:          aws.String("ACTIVE"),
		DesiredCount:    int32(sp.desired),
		LaunchType:      ecstypes.LaunchTypeFargate,
		PlatformVersion: aws.String("LATEST"), PlatformFamily: aws.String("Linux"),
		SchedulingStrategy:   ecstypes.SchedulingStrategyReplica,
		DeploymentController: &ecstypes.DeploymentController{Type: ecstypes.DeploymentControllerTypeEcs},
		TaskDefinition:       aws.String(primaryARN),
		Deployments:          deployments,
		EnableExecuteCommand: sp.exec,
		CreatedAt:            aws.Time(now.Add(-45 * 24 * time.Hour)),
		NetworkConfiguration: serviceNetwork(sp),
		DeploymentConfiguration: &ecstypes.DeploymentConfiguration{
			MaximumPercent: aws.Int32(200), MinimumHealthyPercent: aws.Int32(100),
			DeploymentCircuitBreaker: &ecstypes.DeploymentCircuitBreaker{Enable: true, Rollback: true}},
	}
	if sp.lb && sp.port > 0 {
		svc.LoadBalancers = []ecstypes.LoadBalancer{{
			TargetGroupArn: aws.String("arn:aws:elasticloadbalancing:" + c.region + ":" + c.accountID + ":targetgroup/" + sp.name + "-tg/" + s.hex(16)),
			ContainerName:  aws.String(sp.name), ContainerPort: aws.Int32(int32(sp.port))}}
	}
	s.e.Services = append(s.e.Services, Service{AccountID: c.accountID, Region: c.region, ClusterName: sp.cluster, Service: svc,
		ECRTags: ecrTagsFor(sp), OOMBelowMiB: oomBelowMiB(sp), TaskDefinitionTags: deepCopy(seedTaskDefinitionTags)})

	// Running tasks. With a rollout in flight, the first task is on the new revision.
	for i := 0; i < sp.running; i++ {
		tdARN, depID := primaryARN, primaryID
		started := now.Add(-time.Duration((2 + s.rng.Float64()*70) * float64(time.Hour)))
		if newARN != "" && i > 0 {
			tdARN, depID = currentARN, aws.ToString(deployments[1].Id)
		}
		if newARN != "" && i == 0 {
			started = now.Add(-2 * time.Minute)
		}
		health := ecstypes.HealthStatus("")
		if i < sp.unhealthy {
			health = ecstypes.HealthStatusUnhealthy
		}
		s.addTask(sp, taskOptions{taskDefinitionARN: tdARN, slot: i, startedAt: started, health: health, deploymentID: depID,
			agentPending: sp.name == "auth-service" && i == sp.running-1})
	}

	// Stopped tasks.
	if sp.oom {
		for i := 0; i < 6; i++ {
			at := now.Add(-time.Duration((float64(i)*3.5 + 0.3) * float64(time.Hour)))
			s.addTask(sp, taskOptions{taskDefinitionARN: currentARN, slot: i, startedAt: at.Add(-70 * time.Second), stoppedAt: at,
				reason: "OutOfMemoryError: Container killed due to memory usage", exitCode: 137,
				stopCode: ecstypes.TaskStopCodeEssentialContainerExited, deploymentID: primaryID})
		}
	} else {
		at := now.Add(-time.Duration((20 + s.rng.Float64()*40) * float64(time.Hour)))
		oldDeployment := "ecs-svc/" + s.digits(19)
		s.addTask(sp, taskOptions{taskDefinitionARN: previousARN, slot: 0, startedAt: at.Add(-24 * time.Hour), stoppedAt: at,
			reason: "Scaling activity initiated by (deployment " + oldDeployment + ")", exitCode: 0,
			stopCode: ecstypes.TaskStopCodeServiceSchedulerInitiated, deploymentID: oldDeployment})
		for i := 0; i < 3 && sp.unhealthy > 0; i++ {
			hat := now.Add(-time.Duration((1 + float64(i)*2.5) * float64(time.Hour)))
			s.addTask(sp, taskOptions{taskDefinitionARN: currentARN, slot: i, startedAt: hat.Add(-3 * time.Hour), stoppedAt: hat,
				reason: "Task failed ELB health checks in (target-group " + sp.name + "-tg)", exitCode: 143,
				health: ecstypes.HealthStatusUnhealthy, stopCode: ecstypes.TaskStopCodeServiceSchedulerInitiated, deploymentID: primaryID})
		}
	}

	s.addEvents(sp, svcARN, now)
}

func (s *seeder) addEvents(sp serviceSpec, svcARN string, now time.Time) {
	var events []ecstypes.ServiceEvent
	add := func(minutesAgo float64, msg string) {
		events = append(events, ecstypes.ServiceEvent{Id: aws.String(s.uuid()),
			CreatedAt: aws.Time(now.Add(-time.Duration(minutesAgo * float64(time.Minute)))), Message: aws.String(msg)})
	}
	if sp.deployingTag != "" {
		add(3, fmt.Sprintf("(service %s) has started 1 tasks: (task %s).", sp.name, s.hex(8)))
	}
	if sp.oom {
		add(18, fmt.Sprintf("(service %s) is unable to consistently start tasks successfully.", sp.name))
		add(19, fmt.Sprintf("(service %s) has stopped 2 running tasks: (task %s) (task %s). Reason: OutOfMemoryError.", sp.name, s.hex(8), s.hex(8)))
		add(25, fmt.Sprintf("(service %s) has started 2 tasks: (task %s) (task %s).", sp.name, s.hex(8), s.hex(8)))
	}
	if sp.unhealthy > 0 {
		add(42, fmt.Sprintf("(service %s) (port %d) is unhealthy in (target-group %s-tg) due to (reason Health checks failed with these codes: [503]).", sp.name, sp.port, sp.name))
	}
	add(60*26-6, fmt.Sprintf("(service %s) has reached a steady state.", sp.name))
	if sp.port > 0 && sp.lb {
		add(60*26-3, fmt.Sprintf("(service %s) registered 1 targets in (target-group %s-tg).", sp.name, sp.name))
	}
	add(60*30, fmt.Sprintf("(service %s) has reached a steady state.", sp.name))
	// Newest first, like ECS.
	for i := 1; i < len(events); i++ {
		for j := i; j > 0 && events[j].CreatedAt.After(*events[j-1].CreatedAt); j-- {
			events[j], events[j-1] = events[j-1], events[j]
		}
	}
	s.e.Events[svcARN] = events
}

func (s *seeder) addStandalone() {
	now := s.e.now()
	build := standaloneSpecs[0]
	s.e.TaskDefinitions = append(s.e.TaskDefinitions, s.buildTaskDefinition(build, build.rev-2, bumpTag(build.tag, -2), now.Add(-6*24*time.Hour)))
	buildTD := s.buildTaskDefinition(build, build.rev, build.tag, now.Add(-10*24*time.Hour))
	s.e.TaskDefinitions = append(s.e.TaskDefinitions, buildTD)
	s.addTask(build, taskOptions{taskDefinitionARN: aws.ToString(buildTD.Definition.TaskDefinitionArn), slot: 0,
		startedAt: now.Add(-25 * time.Minute), group: "family:release-build", startedBy: "events-rule/nightly-release"})

	migrate := standaloneSpecs[1]
	s.e.TaskDefinitions = append(s.e.TaskDefinitions, s.buildTaskDefinition(migrate, migrate.rev-2, bumpTag(migrate.tag, -2), now.Add(-8*24*time.Hour)))
	migrateTD := s.buildTaskDefinition(migrate, migrate.rev, migrate.tag, now.Add(-6*24*time.Hour))
	s.e.TaskDefinitions = append(s.e.TaskDefinitions, migrateTD)
	at := now.Add(-2 * time.Hour)
	s.addTask(migrate, taskOptions{taskDefinitionARN: aws.ToString(migrateTD.Definition.TaskDefinitionArn), slot: 0,
		startedAt: at.Add(-4 * time.Minute), stoppedAt: at, reason: "Essential container in task exited", exitCode: 0,
		stopCode: ecstypes.TaskStopCodeEssentialContainerExited, group: "family:db-migrate", startedBy: "pavel"})
}

func serviceNetwork(sp serviceSpec) *ecstypes.NetworkConfiguration {
	var ids []string
	for _, sub := range sp.subnets() {
		ids = append(ids, sub.id)
	}
	assign := ecstypes.AssignPublicIpDisabled
	if sp.public {
		assign = ecstypes.AssignPublicIpEnabled
	}
	return &ecstypes.NetworkConfiguration{AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
		Subnets: ids, SecurityGroups: []string{"sg-0" + fmt.Sprintf("%08x", hashSeed(sp.name, 0)&0xffffffff)}, AssignPublicIp: assign}}
}

// oomBelowMiB is the memory threshold under which a service's tasks are
// OOM-killed: image-resizer needs 1024 MiB, every other service is unaffected.
func oomBelowMiB(sp serviceSpec) int {
	if sp.oom {
		return 1024
	}
	return 0
}
