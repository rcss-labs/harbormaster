package api

import (
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// TagDTO is a tag as the frontend sees it.
type TagDTO struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
	// Inherited is true when the resource has no tag of its own and shows its parent's (or Dev).
	Inherited bool `json:"inherited"`
}

// MeTagDTO is a tag in /api/me, with how many services currently resolve to it.
type MeTagDTO struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Color            string `json:"color"`
	Builtin          bool   `json:"builtin"`
	ServicesInEffect int    `json:"services_in_effect"`
}

// MeAccountDTO is a connected AWS account visible to the caller.
type MeAccountDTO struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	AccountID string   `json:"account_id"`
	Regions   []string `json:"regions"`
}

// MeDTO is the signed-in user with everything the SPA needs to render navigation and permissions.
// Perms maps a tag id (as a string) to its ActionSet bitmask (V=1 L=2 O=4 D=8 E=16 T=32).
type MeDTO struct {
	ID                 int64          `json:"id"`
	Username           string         `json:"username"`
	Admin              bool           `json:"admin"`
	MustChangePassword bool           `json:"must_change_password"`
	CSRFToken          string         `json:"csrf_token"`
	Perms              map[string]int `json:"perms"`
	Tags               []MeTagDTO     `json:"tags"`
	Accounts           []MeAccountDTO `json:"accounts"`
	Regions            []string       `json:"regions"`
	// plan 07: credentials
	HasPassword bool    `json:"has_password"`
	SSOLinked   bool    `json:"sso_linked"`
	BreakGlass  bool    `json:"break_glass"`
	Email       *string `json:"email"`
	DisplayName *string `json:"display_name"`
}

// ListResponse is the body of every paginated list endpoint. Next is null on the last page.
type ListResponse[T any] struct {
	Items    []T       `json:"items"`
	Next     *string   `json:"next"`
	PolledAt time.Time `json:"polled_at"`
}

// ClusterRef identifies a cluster inside another DTO.
type ClusterRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ServiceRef identifies a service and its derived status inside another DTO.
type ServiceRef struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Status model.Status `json:"status"`
}

// PointDTO is one sample of the summed service counts.
type PointDTO struct {
	T       time.Time `json:"t"`
	Running int       `json:"running"`
	Desired int       `json:"desired"`
}

// StopBucketDTO counts tasks stopped within one hour.
type StopBucketDTO struct {
	Hour   time.Time `json:"hour"`
	Normal int       `json:"normal"`
	Error  int       `json:"error"`
}

// OverviewClusterDTO is a cluster with its rolled-up status on the overview.
type OverviewClusterDTO struct {
	ID     string              `json:"id"`
	Name   string              `json:"name"`
	Status model.ClusterStatus `json:"status"`
}

// OverviewServicesDTO counts visible services; ByStatus holds only non-zero statuses.
type OverviewServicesDTO struct {
	Total    int                  `json:"total"`
	ByStatus map[model.Status]int `json:"by_status"`
}

// OverviewTasksDTO sums service tasks (standalone tasks excluded).
type OverviewTasksDTO struct {
	Running    int        `json:"running"`
	Desired    int        `json:"desired"`
	Pending    int        `json:"pending"`
	Unhealthy  int        `json:"unhealthy"`
	History72h []PointDTO `json:"history_72h"`
}

// TaskHealthDTO splits the desired task count into health classes.
type TaskHealthDTO struct {
	Healthy    int `json:"healthy"`
	Unhealthy  int `json:"unhealthy"`
	RollingOut int `json:"rolling_out"`
	Missing    int `json:"missing"`
}

// PerClusterDTO is the running and desired service tasks of one cluster.
type PerClusterDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Running int    `json:"running"`
	Desired int    `json:"desired"`
	// Unhealthy, RollingOut and Missing use the same definitions as TaskHealthDTO.
	Unhealthy  int `json:"unhealthy"`
	RollingOut int `json:"rolling_out"`
	Missing    int `json:"missing"`
}

// DeployDayDTO counts deployment activity on one UTC day.
type DeployDayDTO struct {
	Day     time.Time `json:"day"`
	Deploy  int       `json:"deploy"`
	Restart int       `json:"restart"`
	Failed  int       `json:"failed"`
}

// StopReasonDTO counts stopped tasks of one stop-reason group.
type StopReasonDTO struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// EventDTO is one entry of the activity feed.
type EventDTO struct {
	TS      time.Time  `json:"ts"`
	Source  string     `json:"source"`
	Actor   string     `json:"actor"`
	Service ServiceRef `json:"service"`
	Cluster ClusterRef `json:"cluster"`
	Text    string     `json:"text"`
}

// OverviewDTO is the body of GET /api/overview.
type OverviewDTO struct {
	PolledAt          time.Time            `json:"polled_at"`
	Accounts          int                  `json:"accounts"`
	Regions           int                  `json:"regions"`
	Clusters          []OverviewClusterDTO `json:"clusters"`
	Services          OverviewServicesDTO  `json:"services"`
	Tasks             OverviewTasksDTO     `json:"tasks"`
	TaskHealth        TaskHealthDTO        `json:"task_health"`
	PerCluster        []PerClusterDTO      `json:"per_cluster"`
	StopsPerHour      []StopBucketDTO      `json:"stops_per_hour"`
	DeploymentsPerDay []DeployDayDTO       `json:"deployments_per_day"`
	StopReasons       []StopReasonDTO      `json:"stop_reasons"`
	Events            []EventDTO           `json:"events"`
}

// ClusterCardDTO is a cluster as listed on the clusters page.
type ClusterCardDTO struct {
	ID                string              `json:"id"`
	Name              string              `json:"name"`
	Tag               TagDTO              `json:"tag"`
	Status            model.ClusterStatus `json:"status"`
	AccountID         string              `json:"account_id"`
	AccountName       string              `json:"account_name"`
	Region            string              `json:"region"`
	Services          []ServiceRef        `json:"services"`
	Running           int                 `json:"running"`
	Desired           int                 `json:"desired"`
	ReservedVCPU      float64             `json:"reserved_vcpu"`
	ReservedGiB       float64             `json:"reserved_gib"`
	SpotTasks         int                 `json:"spot_tasks"`
	ContainerInsights bool                `json:"container_insights"`
	LastChangeAt      *time.Time          `json:"last_change_at"`
}

// SlotDTO is one running task of a service. Missing slots (desired - len(slots)) are drawn by the UI.
type SlotDTO struct {
	TaskID     string `json:"task_id"`
	ShortID    string `json:"short_id"`
	PrivateIP  string `json:"private_ip"`
	Health     string `json:"health"`
	RollingOut bool   `json:"rolling_out"`
}

// ClusterServiceDTO is a service row on the cluster page.
type ClusterServiceDTO struct {
	ID                       string       `json:"id"`
	Name                     string       `json:"name"`
	Tag                      TagDTO       `json:"tag"`
	Status                   model.Status `json:"status"`
	Family                   string       `json:"family"`
	Revision                 int          `json:"revision"`
	ImageTag                 string       `json:"image_tag"`
	Running                  int          `json:"running"`
	Desired                  int          `json:"desired"`
	CPU                      string       `json:"cpu"`
	Memory                   string       `json:"memory"`
	Slots                    []SlotDTO    `json:"slots"`
	LatestRegisteredRevision *int         `json:"latest_registered_revision"` // filled by plan 02
	LastChangeAt             *time.Time   `json:"last_change_at"`
}

// ClusterDTO is the body of GET /api/clusters/{clusterId}.
type ClusterDTO struct {
	ClusterCardDTO
	ARN             string              `json:"arn"`
	ConsoleURL      string              `json:"console_url"`
	FargateTasks    int                 `json:"fargate_tasks"`
	TasksHistory72h []PointDTO          `json:"tasks_history_72h"`
	StopsPerHour    []StopBucketDTO     `json:"stops_per_hour"`
	ServicesDetail  []ClusterServiceDTO `json:"services_detail"`
	VPC             *VPCRefDTO          `json:"vpc"` // plan 05: most common VPC of the visible running tasks
	PolledAt        time.Time           `json:"polled_at"`
}

// DeploymentDTO is one ECS deployment of a service.
type DeploymentDTO struct {
	ID                 string    `json:"id"`
	Status             string    `json:"status"`
	Revision           int       `json:"revision"`
	DesiredCount       int       `json:"desired_count"`
	RunningCount       int       `json:"running_count"`
	PendingCount       int       `json:"pending_count"`
	FailedTasks        int       `json:"failed_tasks"`
	RolloutState       string    `json:"rollout_state"`
	RolloutStateReason string    `json:"rollout_state_reason"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// RunningTaskDTO is a running task as listed on the service page.
type RunningTaskDTO struct {
	ID               string `json:"id"`
	ShortID          string `json:"short_id"`
	Health           string `json:"health"`
	AZ               string `json:"az"`
	SubnetID         string `json:"subnet_id"`
	PrivateIP        string `json:"private_ip"`
	Revision         int    `json:"revision"`
	Spot             bool   `json:"spot"`
	ExecAgentRunning bool   `json:"exec_agent_running"`
}

// DeploymentRecordDTO is one row of the recorded deployment history.
type DeploymentRecordDTO struct {
	DeploymentID string     `json:"deployment_id"`
	Revision     int        `json:"revision"`
	Kind         string     `json:"kind"`
	Outcome      string     `json:"outcome"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}

// CircuitBreakerDTO is the service's deployment circuit breaker setting.
type CircuitBreakerDTO struct {
	Enable   bool `json:"enable"`
	Rollback bool `json:"rollback"`
}

// SubnetUseDTO is a service subnet with the number of running tasks in it.
type SubnetUseDTO struct {
	ID    string `json:"id"`
	Tasks int    `json:"tasks"`
}

// LoadBalancerDTO is one target group attachment.
type LoadBalancerDTO struct {
	TargetGroupName string `json:"target_group_name"`
	ContainerName   string `json:"container_name"`
	ContainerPort   int    `json:"container_port"`
}

// NetworkingDTO is the service's network configuration.
type NetworkingDTO struct {
	Subnets        []SubnetUseDTO    `json:"subnets"`
	SecurityGroups []string          `json:"security_groups"`
	AssignPublicIP bool              `json:"assign_public_ip"`
	LoadBalancers  []LoadBalancerDTO `json:"load_balancers"`
	// plan 05: VPC of the service's first subnet (null when unknown) and the names of its security groups.
	VPC                *VPCRefDTO        `json:"vpc"`
	SecurityGroupNames map[string]string `json:"security_group_names"`
}

// VPCRefDTO links a service or cluster to its VPC (plan 05).
type VPCRefDTO struct {
	VPCID string `json:"vpc_id"`
	Name  string `json:"name"`
}

// ContainerDefDTO is a container of the task definition.
type ContainerDefDTO struct {
	Name      string `json:"name"`
	Image     string `json:"image"`
	Essential bool   `json:"essential"`
}

// EFSMountDTO is an EFS volume mounted into a container.
type EFSMountDTO struct {
	FileSystemID  string `json:"file_system_id"`
	AccessPointID string `json:"access_point_id"`
	ContainerPath string `json:"container_path"`
}

// TaskDefinitionDTO summarises the service's task definition.
type TaskDefinitionDTO struct {
	Family      string            `json:"family"`
	Revision    int               `json:"revision"`
	CPU         string            `json:"cpu"`
	Memory      string            `json:"memory"`
	Containers  []ContainerDefDTO `json:"containers"`
	TaskRoleARN string            `json:"task_role_arn"`
	EFSMounts   []EFSMountDTO     `json:"efs_mounts"`
}

// ServiceDTO is the body of GET /api/services/{serviceId}.
type ServiceDTO struct {
	ID                   string                `json:"id"`
	Name                 string                `json:"name"`
	Cluster              ClusterRef            `json:"cluster"`
	AccountID            string                `json:"account_id"`
	AccountName          string                `json:"account_name"`
	Region               string                `json:"region"`
	Tag                  TagDTO                `json:"tag"`
	Status               model.Status          `json:"status"`
	Family               string                `json:"family"`
	Revision             int                   `json:"revision"`
	ImageTag             string                `json:"image_tag"`
	TaskDefinitionARN    string                `json:"task_definition_arn"`
	LaunchType           string                `json:"launch_type"`
	PlatformVersion      string                `json:"platform_version"`
	DeploymentController string                `json:"deployment_controller"`
	Desired              int                   `json:"desired"`
	Running              int                   `json:"running"`
	Pending              int                   `json:"pending"`
	EnableExecuteCommand bool                  `json:"enable_execute_command"`
	MinHealthyPercent    int                   `json:"min_healthy_percent"`
	MaxPercent           int                   `json:"max_percent"`
	CircuitBreaker       CircuitBreakerDTO     `json:"circuit_breaker"`
	Deployments          []DeploymentDTO       `json:"deployments"`
	RunningTasks         []RunningTaskDTO      `json:"running_tasks"`
	Slots                []SlotDTO             `json:"slots"`
	Networking           NetworkingDTO         `json:"networking"`
	TaskDefinition       TaskDefinitionDTO     `json:"task_definition"`
	CountsHistory72h     []PointDTO            `json:"counts_history_72h"`
	Deployments14d       []DeploymentRecordDTO `json:"deployments_14d"`
	RecentEvents         []EventDTO            `json:"recent_events"`
	Can                  map[string]bool       `json:"can"`
	ConsoleURL           string                `json:"console_url"`
	PolledAt             time.Time             `json:"polled_at"`
	Controller           ControllerDTO         `json:"controller"`
	StrategyText         string                `json:"strategy_text"`
}

// TaskCanDTO is what the caller may do with one task.
type TaskCanDTO struct {
	Operate  bool `json:"operate"`
	Terminal bool `json:"terminal"`
}

// TaskDTO is one task in a task list. Service is null for standalone tasks.
type TaskDTO struct {
	ID              string      `json:"id"`
	ShortID         string      `json:"short_id"`
	ARN             string      `json:"arn"`
	Service         *ServiceRef `json:"service"`
	Cluster         ClusterRef  `json:"cluster"`
	Group           string      `json:"group"`
	StartedBy       string      `json:"started_by"`
	Status          string      `json:"status"`
	ExitCode        *int        `json:"exit_code"`
	Health          string      `json:"health"`
	Family          string      `json:"family"`
	Revision        int         `json:"revision"`
	PrivateIP       string      `json:"private_ip"`
	SubnetID        string      `json:"subnet_id"`
	AZ              string      `json:"az"`
	Capacity        string      `json:"capacity"`
	CPU             string      `json:"cpu"`
	Memory          string      `json:"memory"`
	StartedAt       *time.Time  `json:"started_at"`
	StoppedAt       *time.Time  `json:"stopped_at"`
	StopCode        string      `json:"stop_code"`
	StoppedReason   string      `json:"stopped_reason"`
	StopReasonGroup string      `json:"stop_reason_group"`
	ExecEnabled     bool        `json:"exec_enabled"`
	Can             TaskCanDTO  `json:"can"`
}

// TaskCountsDTO counts tasks per filter, ignoring pagination.
type TaskCountsDTO struct {
	Problems int `json:"problems"`
	Running  int `json:"running"`
	Stopped  int `json:"stopped"`
	All      int `json:"all"`
}

// TaskStatusSummaryDTO splits tasks by running health and recent stop outcome.
type TaskStatusSummaryDTO struct {
	RunningHealthy   int `json:"running_healthy"`
	RunningUnhealthy int `json:"running_unhealthy"`
	StoppedError     int `json:"stopped_error"`
	StoppedNormal    int `json:"stopped_normal"`
}

// AZCountDTO counts running tasks in one availability zone.
type AZCountDTO struct {
	AZ    string `json:"az"`
	Count int    `json:"count"`
}

// ClusterCapacityDTO counts a cluster's running tasks by Fargate capacity.
type ClusterCapacityDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Fargate int    `json:"fargate"`
	Spot    int    `json:"spot"`
}

// TaskSummaryDTO is the aggregate block of a task list.
type TaskSummaryDTO struct {
	ByStatus    TaskStatusSummaryDTO `json:"by_status"`
	ByAZ        []AZCountDTO         `json:"by_az"`
	ByCluster   []ClusterCapacityDTO `json:"by_cluster"`
	StopReasons []StopReasonDTO      `json:"stop_reasons"`
}

// TasksListDTO is the body of GET /api/tasks and GET /api/services/{id}/tasks.
type TasksListDTO struct {
	Items    []TaskDTO      `json:"items"`
	Next     *string        `json:"next"`
	PolledAt time.Time      `json:"polled_at"`
	Counts   TaskCountsDTO  `json:"counts"`
	Summary  TaskSummaryDTO `json:"summary"`
}

// SearchItemDTO is one global-search result.
type SearchItemDTO struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Sub   string `json:"sub"`
	Href  string `json:"href"`
}

// PollerItemDTO is the aggregated poll status of one account and region.
type PollerItemDTO struct {
	AccountID   string     `json:"account_id"`
	AccountName string     `json:"account_name"`
	Region      string     `json:"region"`
	Throttled   bool       `json:"throttled"`
	Failing     bool       `json:"failing"`
	LastError   string     `json:"last_error"`
	LastOKAt    *time.Time `json:"last_ok_at"`
}

// ItemsResponse is the body of the unpaginated list endpoints: {"items": [...]}.
type ItemsResponse[T any] struct {
	Items []T `json:"items"`
}

// SetupStatusDTO is the body of GET /api/auth/setup.
type SetupStatusDTO struct {
	Needed bool `json:"needed"`
}

// LoginResponse is the body of a successful sign-in.
type LoginResponse struct {
	User MeDTO `json:"user"`
}

// CreateUserResponse is the body of POST /api/users. Fields are in key order so the JSON matches a sorted map.
type CreateUserResponse struct {
	TemporaryPassword string  `json:"temporary_password"`
	User              UserDTO `json:"user"`
}

// AccountTestResponse is the body of POST /api/accounts/test.
type AccountTestResponse struct {
	Regions []RegionResultDTO `json:"regions"`
}

// PermissionsResponse is the body of GET /api/permissions.
type PermissionsResponse struct {
	Tags  []permissionTagDTO  `json:"tags"`
	Users []permissionUserDTO `json:"users"`
}

// TagImpactResponse is the body of GET /api/tags/{id}/impact.
type TagImpactResponse struct {
	Clusters []idNameDTO `json:"clusters"`
	Services []idNameDTO `json:"services"`
	Users    []string    `json:"users"`
}

// TagAssignmentsResponse is the body of GET /api/tag-assignments.
type TagAssignmentsResponse struct {
	Clusters []clusterAssignmentDTO `json:"clusters"`
}

// SettingsDTO is the body of GET and PUT /api/settings.
type SettingsDTO struct {
	StoppedTasksWindowMinutes int `json:"stopped_tasks_window_minutes"`
}

// CertDTO describes a certificate without exposing its PEM. Fingerprint is the colon-separated SHA-256.
type CertDTO struct {
	Fingerprint string    `json:"fingerprint"`
	Subject     string    `json:"subject"`
	NotAfter    time.Time `json:"not_after"`
}

// SSOConfigDTO is the body of the admin SSO endpoints.
type SSOConfigDTO struct {
	Configured                   bool       `json:"configured"`
	Enabled                      bool       `json:"enabled"`
	IdPEntityID                  string     `json:"idp_entity_id"`
	IdPSSORedirectURL            string     `json:"idp_sso_redirect_url"`
	IdPCerts                     []CertDTO  `json:"idp_certs"`
	MetadataURL                  *string    `json:"metadata_url"`
	MetadataFetchedAt            *time.Time `json:"metadata_fetched_at"`
	SPEntityID                   string     `json:"sp_entity_id"`
	ACSURL                       string     `json:"acs_url"`
	MetadataPath                 string     `json:"metadata_path"`
	SPCert                       *CertDTO   `json:"sp_cert"`
	SPKeyState                   string     `json:"sp_key_state"` // "ok" | "missing" | "undecryptable"
	NameIDFormat                 string     `json:"name_id_format"`
	AttrEmail                    *string    `json:"attr_email"`
	AttrDisplayName              *string    `json:"attr_display_name"`
	AttrUsername                 *string    `json:"attr_username"`
	ClockSkewSeconds             int        `json:"clock_skew_seconds"`
	ButtonLabel                  string     `json:"button_label"`
	BreakGlassAdmins             int        `json:"break_glass_admins"`
	PasswordUsersBlockedOnEnable int        `json:"password_users_blocked_on_enable"`
	Warnings                     []string   `json:"warnings"`
}

// SSOTestResultDTO is the one-time result of a Test sign-in.
type SSOTestResultDTO struct {
	OK           bool                `json:"ok"`
	Reason       string              `json:"reason"`
	Message      string              `json:"message"`
	NameID       string              `json:"name_id"`
	NameIDFormat string              `json:"name_id_format"`
	Attributes   map[string][]string `json:"attributes"`
	Username     string              `json:"username"`
	Outcome      string              `json:"outcome"` // "would_create" | "would_sign_in" | "would_link" | "" on failure
	ExistingUser *string             `json:"existing_user"`
}
