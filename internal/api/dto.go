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
	PolledAt        time.Time           `json:"polled_at"`
}
