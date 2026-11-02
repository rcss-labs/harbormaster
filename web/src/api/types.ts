// TypeScript mirrors of the JSON DTOs served by the Go backend (internal/api/dto.go).
// Shapes follow the API reference; keys are snake_case, times are RFC3339 strings.

export type Status = 'steady' | 'deploying' | 'degraded' | 'failing' | 'unhealthy_task' | 'scaled_to_zero'
export type ClusterStatus = 'healthy' | 'degraded' | 'deploying' | 'needs_attention'
export type TagColor = 'neutral' | 'red' | 'amber' | 'blue' | 'violet' | 'green'
export type Health = 'HEALTHY' | 'UNHEALTHY' | 'UNKNOWN'
/** FARGATE, FARGATE_SPOT, EXTERNAL, or the name of an EC2 capacity provider. */
export type Capacity = string
export type TaskStatus =
  | 'PROVISIONING'
  | 'PENDING'
  | 'ACTIVATING'
  | 'RUNNING'
  | 'DEACTIVATING'
  | 'STOPPING'
  | 'DEPROVISIONING'
  | 'STOPPED'

export type ActionKey = 'view' | 'logs' | 'operate' | 'deploy' | 'edit' | 'terminal'
/** Permission bits per action: view=1 logs=2 operate=4 deploy=8 edit=16 terminal=32. */
export const ACTION_BITS: Record<ActionKey, number> = { view: 1, logs: 2, operate: 4, deploy: 8, edit: 16, terminal: 32 }

export type ErrorCode =
  | 'unauthenticated'
  | 'forbidden'
  | 'csrf'
  | 'password_change_required'
  | 'bad_setup_token'
  | 'not_found'
  | 'conflict'
  | 'invalid'
  | 'throttled'
  | 'aws_error'

export interface ErrorBody {
  error: { code: ErrorCode | string; message: string }
}

/** Generic list body: `{items, next, polled_at}`. */
export interface ListDTO<T> {
  items: T[]
  next: string | null
  polled_at: string
}

// ---- Auth ----

export interface MeTagDTO {
  id: number
  name: string
  color: TagColor
  builtin: boolean
  services_in_effect: number
}

export interface MeAccountDTO {
  id: number
  name: string
  account_id: string
  regions: string[]
}

export interface MeDTO {
  id: number
  username: string
  admin: boolean
  must_change_password: boolean
  csrf_token: string
  perms: Record<string, number>
  tags: MeTagDTO[]
  accounts: MeAccountDTO[]
  regions: string[]
  has_password: boolean
  sso_linked: boolean
  break_glass: boolean
  email: string | null
  display_name: string | null
}

export interface SetupStatusDTO {
  needed: boolean
}

// ---- Shared ----

export interface TagDTO {
  id: number
  name: string
  color: TagColor
  inherited: boolean
}
export interface ClusterRef {
  id: string
  name: string
}
export interface ServiceRef {
  id: string
  name: string
  /** `''` for an event with no service, or one whose service is gone. */
  status: Status | ''
}
export interface PointDTO {
  t: string
  running: number
  desired: number
}
export interface EventDTO {
  ts: string
  source: 'ecs' | 'harbormaster'
  actor: string
  service: ServiceRef
  cluster: ClusterRef
  text: string
}
export interface SlotDTO {
  task_id: string
  short_id: string
  private_ip: string
  health: Health
  rolling_out: boolean
}
export interface StopsHourDTO {
  hour: string
  normal: number
  error: number
}
export interface DeploysDayDTO {
  day: string
  deploy: number
  restart: number
  failed: number
}
export interface ReasonCountDTO {
  reason: string
  count: number
}

// ---- Overview ----

export interface OverviewDTO {
  polled_at: string
  accounts: number
  regions: number
  clusters: { id: string; name: string; status: ClusterStatus }[]
  services: { total: number; by_status: Partial<Record<Status, number>> }
  tasks: { running: number; desired: number; pending: number; unhealthy: number; history_72h: PointDTO[] }
  task_health: { healthy: number; unhealthy: number; rolling_out: number; missing: number }
  per_cluster: {
    id: string
    name: string
    running: number
    desired: number
    unhealthy: number
    rolling_out: number
    missing: number
  }[]
  stops_per_hour: StopsHourDTO[]
  deployments_per_day: DeploysDayDTO[]
  stop_reasons: ReasonCountDTO[]
  events: EventDTO[]
}

// ---- Clusters ----

export interface ClusterCardDTO {
  id: string
  name: string
  tag: TagDTO
  status: ClusterStatus
  account_id: string
  account_name: string
  region: string
  services: ServiceRef[]
  running: number
  desired: number
  reserved_vcpu: number
  reserved_gib: number
  spot_tasks: number
  container_insights: boolean
  last_change_at: string | null
}

export interface ClusterServiceDTO {
  id: string
  name: string
  tag: TagDTO
  status: Status
  family: string
  revision: number
  image_tag: string
  running: number
  desired: number
  cpu: string
  memory: string
  slots: SlotDTO[]
  latest_registered_revision: number | null
  last_change_at: string | null
}

export interface ClusterDTO extends ClusterCardDTO {
  arn: string
  console_url: string
  fargate_tasks: number
  tasks_history_72h: PointDTO[]
  stops_per_hour: StopsHourDTO[]
  services_detail: ClusterServiceDTO[]
  vpc: VPCRefDTO | null
  polled_at: string
}

export interface VPCRefDTO {
  vpc_id: string
  name: string
}

// ---- Services ----

export interface DeploymentDTO {
  id: string
  status: 'PRIMARY' | 'ACTIVE' | 'INACTIVE'
  revision: number
  desired_count: number
  running_count: number
  pending_count: number
  failed_tasks: number
  rollout_state: 'IN_PROGRESS' | 'COMPLETED' | 'FAILED'
  rollout_state_reason: string
  created_at: string
  updated_at: string
}

export interface RunningTaskDTO {
  id: string
  short_id: string
  health: Health
  az: string
  subnet_id: string
  private_ip: string
  revision: number
  spot: boolean
  exec_agent_running: boolean
}

export interface DeploymentRecordDTO {
  deployment_id: string
  revision: number
  kind: 'deploy' | 'restart' | 'rollback'
  outcome: '' | 'succeeded' | 'failed' | 'rolled_back'
  started_at: string
  finished_at: string | null
}

export interface ServiceDTO {
  id: string
  name: string
  cluster: ClusterRef
  account_id: string
  account_name: string
  region: string
  tag: TagDTO
  status: Status
  family: string
  revision: number
  image_tag: string
  task_definition_arn: string
  launch_type: string
  platform_version: string
  deployment_controller: string
  desired: number
  running: number
  pending: number
  enable_execute_command: boolean
  min_healthy_percent: number
  max_percent: number
  circuit_breaker: { enable: boolean; rollback: boolean }
  deployments: DeploymentDTO[]
  running_tasks: RunningTaskDTO[]
  slots: SlotDTO[]
  networking: {
    subnets: { id: string; tasks: number }[]
    security_groups: string[]
    assign_public_ip: boolean
    load_balancers: { target_group_name: string; container_name: string; container_port: number }[]
    vpc: VPCRefDTO | null
    security_group_names: Record<string, string>
  }
  task_definition: {
    family: string
    revision: number
    cpu: string
    memory: string
    containers: { name: string; image: string; essential: boolean }[]
    task_role_arn: string
    efs_mounts: { file_system_id: string; access_point_id: string; container_path: string }[]
  }
  counts_history_72h: PointDTO[]
  deployments_14d: DeploymentRecordDTO[]
  recent_events: EventDTO[]
  can: Record<ActionKey, boolean>
  console_url: string
  polled_at: string
  controller: ControllerDTO
  strategy_text: string
}

// ---- Tasks ----

export interface TaskDTO {
  id: string
  short_id: string
  arn: string
  service: ServiceRef | null
  cluster: ClusterRef
  group: string
  started_by: string
  status: string
  exit_code: number | null
  health: Health
  family: string
  revision: number
  private_ip: string
  subnet_id: string
  az: string
  capacity: Capacity
  cpu: string
  memory: string
  started_at: string | null
  stopped_at: string | null
  stop_code: string
  stopped_reason: string
  stop_reason_group: string
  exec_enabled: boolean
  can: { operate: boolean; terminal: boolean }
}

export type TasksFilter = 'problems' | 'running' | 'stopped' | 'all'

export interface TasksListDTO {
  items: TaskDTO[]
  next: string | null
  polled_at: string
  counts: { problems: number; running: number; stopped: number; all: number }
  summary: {
    by_status: { running_healthy: number; running_unhealthy: number; stopped_error: number; stopped_normal: number }
    by_az: { az: string; count: number }[]
    by_cluster: { id: string; name: string; fargate: number; spot: number }[]
    stop_reasons: ReasonCountDTO[]
  }
}

// ---- Search, operations, poller ----

export interface SearchResultDTO {
  kind: 'cluster' | 'service' | 'task'
  label: string
  sub: string
  href: string
}
export interface SearchDTO {
  items: SearchResultDTO[]
}

export type OperationsDTO = ListDTO<EventDTO>

export interface PollerEntryDTO {
  account_id: string
  account_name: string
  region: string
  throttled: boolean
  failing: boolean
  last_error: string
  last_ok_at: string | null
}
export interface PollerDTO {
  items: PollerEntryDTO[]
}

// ---- Admin: users and permissions ----

export type SSOState = 'none' | 'linked' | 'pending' | 'pending_expired'

export interface UserDTO {
  id: number
  username: string
  admin: boolean
  must_change_password: boolean
  last_sign_in_at: string | null
  can_see: TagDTO[]
  has_password: boolean
  break_glass: boolean
  sso: SSOState
  /** Linked or pending NameID, the admin-visible identifier. */
  sso_name_id: string | null
  pending_link_expires_at: string | null
  email: string | null
  display_name: string | null
}
export interface UsersDTO {
  items: UserDTO[]
}
export interface CreateUserResponse {
  user: UserDTO
  temporary_password: string
}
export interface PermissionsDTO {
  tags: { id: number; name: string; color: TagColor; services_in_effect: number }[]
  users: { id: number; username: string; admin: boolean; perms: Record<string, number> }[]
}

// ---- Admin: tags and assignments ----

export interface TagItemDTO {
  id: number
  name: string
  color: TagColor
  builtin: boolean
  clusters: number
  services_direct: number
  services_effective: number
  users: { username: string; preset: string }[]
}
export interface TagsDTO {
  items: TagItemDTO[]
}
export interface TagImpactDTO {
  clusters: { id: string; name: string }[]
  services: { id: string; name: string }[]
  users: string[]
}
export interface TagAssignmentsDTO {
  clusters: {
    id: string
    name: string
    account_name: string
    region: string
    tag_id: number | null
    effective: TagDTO
    services: { id: string; name: string; tag_id: number | null; effective: TagDTO }[]
  }[]
}

// ---- Admin: AWS accounts and audit ----

export interface AccountDTO {
  id: number
  name: string
  account_id: string
  role_arn: string
  external_id: string
  regions: string[]
  enabled: boolean
  clusters: number
  status: 'connected' | 'error' | 'disabled'
  last_ok_at: string | null
  last_error: string
}
export interface AccountsDTO {
  items: AccountDTO[]
  runtime: { cluster: string; service: string; intervals: Record<string, string> }
}
export interface RegionResult {
  region: string
  ok: boolean
  clusters: number
  error: string
}
export interface AuditEntryDTO {
  id: number
  ts: string
  actor: string
  action: string
  target: string
  summary: string
  details: Record<string, unknown> | null
  ip: string
}
export type AuditDTO = ListDTO<AuditEntryDTO>

// ---- Observe (plan 03): metrics, target health, scaling, alerts, log history. Mirrors internal/api/observe_*.go. ----
export type Range = '1h' | '6h' | '24h' | '72h'
/** A metric series; `null` is a missing datapoint. */
export type Series = (number | null)[]
export interface StaleInfo {
  reason: string
  fetched_at: string
}
export type InsightsMode = 'disabled' | 'enabled' | 'enhanced'
export interface MetricMarkerDTO {
  at: string
  label: string
  kind: 'deploy' | 'restart'
  failed: boolean
  /** Set only on cluster charts. */
  service?: string
}
export interface ServiceMetricsDTO {
  range: Range
  period: number
  insights: InsightsMode
  timestamps: string[]
  series: { cpu: Series; mem: Series; net_rx_kbps: Series; net_tx_kbps: Series; tasks: Series } | null
  now: { cpu: number | null; mem: number | null; cpu_units: number; mem_mib: number; running: number } | null
  alb: {
    req_per_min: Series
    p95_ms: Series
    err5xx_per_hour: { timestamps: string[]; values: Series }
    req_now: number | null
    p95_now: number | null
    err5xx_today: number
  } | null
  tasks_now: { task_id: string; cpu: number | null; mem: number | null }[] | null
  markers: MetricMarkerDTO[]
  stale: StaleInfo | null
  polled_at: string | null
}
export interface ClusterMetricsDTO {
  range: Range
  period: number
  insights: string
  timestamps: string[]
  cluster: { cpu: Series; mem: Series } | null
  now: { cpu: number | null; mem: number | null } | null
  markers: MetricMarkerDTO[]
  services: Record<
    string,
    {
      cpu: Series
      mem: Series
      cpu_now: number | null
      mem_now: number | null
      alb: { req_now: number | null; p95_now: number | null; err5xx_today: number } | null
    }
  >
  stale: StaleInfo | null
}
/** One account x region whose metrics could not be read; its clusters have no series. */
export interface TargetErrorDTO {
  account_id: string
  region: string
  code: string
  message: string
}
export interface ClustersMetricsDTO {
  items: Record<string, { insights: string; cpu: Series; mem: Series; cpu_now: number | null; mem_now: number | null }>
  stale: StaleInfo | null
  errors: TargetErrorDTO[]
}
export interface ClusterSeriesDTO {
  id: string
  label: string
  values: Series
}
export interface OverviewMetricsDTO {
  timestamps: string[]
  cpu_by_cluster: ClusterSeriesDTO[]
  mem_by_cluster: ClusterSeriesDTO[]
  avg: { cpu: number | null; mem: number | null }
  stale: StaleInfo | null
  errors: TargetErrorDTO[]
}
export interface TargetDTO {
  id: string
  port: number
  az: string
  state: string
  reason: string
  description: string
}
export interface TargetHealthDTO {
  groups: { target_group_arn: string; name: string; healthy: number; unhealthy: number; targets: TargetDTO[] }[]
  healthy: number
  unhealthy: number
  polled_at: string | null
}
export interface ScalingPolicyDTO {
  name: string
  type: string
  metric: string
  target_value: number
  scale_out_cooldown: number
  scale_in_cooldown: number
}
export interface ScheduledActionDTO {
  name: string
  schedule: string
  timezone: string
  min_capacity: number | null
  max_capacity: number | null
}
export interface ScalingActivityDTO {
  id: string
  description: string
  cause: string
  status_code: string
  status_message: string
  started_at: string
  ended_at: string | null
}
export interface ScalingDTO {
  has_target: boolean
  min: number
  max: number
  policies: ScalingPolicyDTO[]
  scheduled_actions: ScheduledActionDTO[]
  activities: ScalingActivityDTO[]
  polled_at: string | null
}
export interface AlertDTO {
  kind: string
  severity: 'danger' | 'warning' | 'info'
  title: string
  text: string
  at: string
  service_id: string
  service: string
  cluster: string
  cluster_id: string
  tab: string
}
export interface AlertsDTO {
  items: AlertDTO[]
  counts: { danger: number; warning: number; info: number }
  polled_at: string | null
}
export interface LogLine {
  t: string
  container: string
  task_id: string
  message: string
}
export interface LogContainerDTO {
  name: string
  driver: string
  supported: boolean
  stream_prefix?: string
  reason?: 'driver' | 'other_group'
}
export interface LogsHistoryDTO {
  group: string
  region: string
  containers: LogContainerDTO[]
  lines: LogLine[]
  older_before: string | null
  truncated: boolean
}

// ---- Change-path reads (plan 02) ----
export interface ControllerRefDTO {
  type: string
  supported: boolean
  name: string
}
export interface ControllerDTO extends ControllerRefDTO {
  banner: string
}
export interface TaskDefinitionResponse {
  family: string
  deployed_revision: number
  task_definition_arn: string
  main_container: string
  /** The register-ready definition; JSON.stringify(_, null, 2) keeps the server's key order. */
  definition: Record<string, unknown>
  tags: { key: string; value: string }[]
  controller: ControllerRefDTO
  strategy: string
  can: { edit: boolean; deploy: boolean; operate: boolean; admin: boolean }
  tag: TagDTO
  polled_at: string
}
export interface RevisionDTO {
  revision: number
  arn: string
  status: 'deployed' | 'rolling_out' | 'previous' | 'not_deployed'
  image_tag: string
  cpu: string
  memory: string
  registered_at: string
  registered_by: string
}
export interface RevisionsResponse {
  items: RevisionDTO[]
  deployed_revision: number
  rolling_out_revision: number | null
}
export interface ImageTagsResponse {
  source: 'ecr' | 'none'
  repository: string
  current: string
  tags: { tag: string; pushed_at: string }[]
}
export interface DesiredCountResponse {
  desired: number
  running: number
  max: number
  auto_scaling: { min: number; max: number } | null
}
export interface SettingsDTO {
  stopped_tasks_window_minutes: number
}

// ---- Single sign-on (plan 07). Mirrors internal/api/saml_public.go and dto.go. ----

/** Public `GET /api/auth/options`: `sso` is null until a configuration row exists. */
export interface AuthOptionsDTO {
  sso: { enabled: boolean; label: string } | null
  password_login: 'all' | 'break_glass'
}

export interface CertDTO {
  fingerprint: string
  subject: string
  not_after: string
}

export type SPKeyState = 'ok' | 'missing' | 'undecryptable'

/** Admin `GET /api/sso` and the bodies of the other admin SSO endpoints. The IdP PEM is never sent back. */
export interface SSOConfigDTO {
  configured: boolean
  enabled: boolean
  idp_entity_id: string
  idp_sso_redirect_url: string
  idp_certs: CertDTO[]
  metadata_url: string | null
  metadata_fetched_at: string | null
  sp_entity_id: string
  acs_url: string
  metadata_path: string
  sp_cert: CertDTO | null
  sp_key_state: SPKeyState
  name_id_format: string
  attr_email: string | null
  attr_display_name: string | null
  attr_username: string | null
  clock_skew_seconds: number
  button_label: string
  break_glass_admins: number
  password_users_blocked_on_enable: number
  warnings: string[]
}

export type SSOTestOutcome = 'would_create' | 'would_sign_in' | 'would_link'

/** One-time result of a Test sign-in. `outcome` is empty when the test failed. */
export interface SSOTestResultDTO {
  ok: boolean
  reason: string
  message: string
  name_id: string
  name_id_format: string
  attributes: Record<string, string[]>
  username: string
  outcome: SSOTestOutcome | ''
  existing_user: string | null
}
