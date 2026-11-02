// Deterministic API fixtures shaped like the fake estate (two accounts, six clusters, fifteen services).
// Every body is derived from one set of service definitions so counts, ids and statuses agree across screens.
import type {
  AccountsDTO,
  AlertsDTO,
  AuditDTO,
  AuthOptionsDTO,
  CertDTO,
  ClusterCardDTO,
  ClusterDTO,
  ClusterMetricsDTO,
  ClusterServiceDTO,
  ClusterStatus,
  ClustersMetricsDTO,
  DeploysDayDTO,
  EventDTO,
  ListDTO,
  LogsHistoryDTO,
  MeDTO,
  OverviewDTO,
  OverviewMetricsDTO,
  PermissionsDTO,
  PointDTO,
  PollerDTO,
  ReasonCountDTO,
  ScalingDTO,
  Series,
  ServiceDTO,
  ServiceMetricsDTO,
  SlotDTO,
  Status,
  StopsHourDTO,
  TagAssignmentsDTO,
  TagColor,
  TagDTO,
  TaskDefinitionResponse,
  RevisionsResponse,
  ImageTagsResponse,
  DesiredCountResponse,
  TagsDTO,
  TargetHealthDTO,
  TaskDTO,
  SettingsDTO,
  SSOConfigDTO,
  SSOTestResultDTO,
  TasksListDTO,
  UsersDTO,
} from '../api/types'
import type {
  AnalyticsCapacity,
  AnalyticsDelivery,
  AnalyticsOperations,
  AnalyticsRange,
  AnalyticsReliability,
  AnalyticsSeries,
  AnalyticsSource,
  AnalyticsWindow,
  Proposed,
} from '../api/analytics'

/** Reference "now" for every fixture timestamp; tests can freeze the clock here. */
export const FIXTURE_NOW = '2026-10-31T12:00:00Z'
const NOW_MS = Date.parse(FIXTURE_NOW)

/** RFC3339 timestamp `minutes` before FIXTURE_NOW. */
export function minutesAgo(minutes: number): string {
  return new Date(NOW_MS - minutes * 60_000).toISOString().replace('.000Z', 'Z')
}

// ---- ids: base64url (no padding) of the pipe-joined key, exactly like the Go ID() helpers ----

export function base64url(text: string): string {
  return btoa(text).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}
export const clusterId = (account: string, region: string, cluster: string) => base64url(`${account}|${region}|${cluster}`)
export const serviceId = (account: string, region: string, cluster: string, service: string) =>
  base64url(`${account}|${region}|${cluster}|${service}`)
export const taskId = (account: string, region: string, taskArn: string) => base64url(`${account}|${region}|${taskArn}`)

/** Stable pseudo-random hex string, so ARNs and IPs never change between runs. */
function hex(seed: string, length: number): string {
  let state = 2166136261
  for (const char of seed) state = Math.imul(state ^ char.charCodeAt(0), 16777619) >>> 0
  let out = ''
  while (out.length < length) {
    state = Math.imul(state ^ (state >>> 15), 2246822519) >>> 0
    out += state.toString(16).padStart(8, '0')
  }
  return out.slice(0, length)
}

// ---- tags, accounts, regions ----

const tag = (id: number, name: string, color: TagColor, inherited = false): TagDTO => ({ id, name, color, inherited })
export const TAG_DEV = tag(1, 'Dev', 'neutral')
export const TAG_STAGING = tag(2, 'Staging', 'amber')
export const TAG_PROD = tag(3, 'Prod', 'red')
const TAG_BY_NAME: Record<string, TagDTO> = { Dev: TAG_DEV, Staging: TAG_STAGING, Prod: TAG_PROD }

export const ACCOUNT_HOMELAB = { id: 1, name: 'homelab-admin', account_id: '482109375512' }
export const ACCOUNT_STAGING = { id: 2, name: 'staging-example', account_id: '731022849906' }
const REGIONS = ['us-east-1', 'eu-west-1', 'eu-central-1']

const AZ_SUBNETS: Record<string, [string, string][]> = {
  'us-east-1': [['us-east-1a', 'subnet-0a11b2c3'], ['us-east-1b', 'subnet-0b22c3d4']],
  'eu-west-1': [['eu-west-1a', 'subnet-0c33d4e5'], ['eu-west-1b', 'subnet-0d44e5f6']],
  'eu-central-1': [['eu-central-1a', 'subnet-0e55f6a7'], ['eu-central-1b', 'subnet-0f66a7b8']],
}

// ---- cluster and service definitions (service rows follow the mockup's SERVICE_SRC) ----

interface ClusterDef {
  name: string
  account: typeof ACCOUNT_HOMELAB
  region: string
  tag: string
  insights: boolean
  status: ClusterStatus
}
const CLUSTER_DEFS: ClusterDef[] = [
  { name: 'core-services', account: ACCOUNT_HOMELAB, region: 'us-east-1', tag: 'Prod', insights: true, status: 'deploying' },
  { name: 'background-jobs', account: ACCOUNT_HOMELAB, region: 'us-east-1', tag: 'Dev', insights: true, status: 'needs_attention' },
  { name: 'ci-tools', account: ACCOUNT_HOMELAB, region: 'us-east-1', tag: 'Dev', insights: true, status: 'healthy' },
  { name: 'eu-apps', account: ACCOUNT_HOMELAB, region: 'eu-west-1', tag: 'Dev', insights: true, status: 'degraded' },
  { name: 'edge-proxy', account: ACCOUNT_HOMELAB, region: 'eu-central-1', tag: 'Dev', insights: false, status: 'healthy' },
  { name: 'staging-web', account: ACCOUNT_STAGING, region: 'us-east-1', tag: 'Staging', insights: true, status: 'healthy' },
]

interface ServiceDef {
  cluster: string
  name: string
  desired: number
  running: number
  revision: number
  repo: string
  imageTag: string
  cpu: number
  memory: number
  port: number
  lb?: boolean
  exec: boolean
  spot?: boolean
  efs?: { fileSystemId: string; accessPointId: string; path: string }
  status: Status
}
const steady = { status: 'steady' as Status, exec: true }
const SERVICE_DEFS: ServiceDef[] = [
  { cluster: 'core-services', name: 'payments-api', desired: 3, running: 3, revision: 42, repo: 'payments-api', imageTag: '1.18.2', cpu: 512, memory: 1024, port: 8080, lb: true, ...steady },
  { cluster: 'core-services', name: 'web-frontend', desired: 2, running: 2, revision: 87, repo: 'web-frontend', imageTag: '3.4.0', cpu: 256, memory: 512, port: 3000, lb: true, ...steady },
  { cluster: 'core-services', name: 'auth-service', desired: 2, running: 2, revision: 15, repo: 'auth-service', imageTag: '0.9.7', cpu: 256, memory: 512, port: 9000, lb: true, ...steady },
  // Mid-rollout: revision 10 is PRIMARY, revision 9 still ACTIVE, one running task on each.
  { cluster: 'core-services', name: 'notifications', desired: 2, running: 2, revision: 10, repo: 'notifications', imageTag: '2.3.0', cpu: 256, memory: 512, port: 8081, exec: true, status: 'deploying' },
  { cluster: 'background-jobs', name: 'report-worker', desired: 1, running: 1, revision: 31, repo: 'report-worker', imageTag: '5.0.1', cpu: 1024, memory: 2048, port: 0, spot: true, efs: { fileSystemId: 'fs-0b7c11d2', accessPointId: 'fsap-05e1c7a9b2', path: '/data/reports' }, ...steady },
  { cluster: 'background-jobs', name: 'email-sender', desired: 1, running: 1, revision: 12, repo: 'email-sender', imageTag: '1.3.3', cpu: 256, memory: 512, port: 0, ...steady },
  { cluster: 'background-jobs', name: 'image-resizer', desired: 2, running: 0, revision: 6, repo: 'image-resizer', imageTag: '0.4.2', cpu: 256, memory: 512, port: 8090, exec: false, status: 'failing' },
  { cluster: 'ci-tools', name: 'jenkins-controller', desired: 1, running: 1, revision: 58, repo: 'jenkins', imageTag: '2.479.1-lts', cpu: 2048, memory: 4096, port: 8080, lb: true, efs: { fileSystemId: 'fs-09d4e7a1', accessPointId: 'fsap-0a93f1c4d7', path: '/var/jenkins_home' }, ...steady },
  { cluster: 'ci-tools', name: 'artifact-proxy', desired: 1, running: 1, revision: 7, repo: 'artifact-proxy', imageTag: '1.0.4', cpu: 512, memory: 1024, port: 8081, spot: true, ...steady },
  { cluster: 'eu-apps', name: 'catalog-api', desired: 2, running: 2, revision: 23, repo: 'catalog-api', imageTag: '4.1.0', cpu: 512, memory: 1024, port: 8080, lb: true, exec: true, status: 'unhealthy_task' },
  { cluster: 'eu-apps', name: 'search-indexer', desired: 1, running: 1, revision: 14, repo: 'search-indexer', imageTag: '2.0.3', cpu: 1024, memory: 4096, port: 0, efs: { fileSystemId: 'fs-0d18c2ab', accessPointId: 'fsap-03c7a9e5d1', path: '/index' }, ...steady },
  { cluster: 'edge-proxy', name: 'edge-nginx', desired: 2, running: 2, revision: 19, repo: 'edge-nginx', imageTag: '1.27.2', cpu: 256, memory: 512, port: 443, lb: true, efs: { fileSystemId: 'fs-0a6b33f0', accessPointId: 'fsap-0e44d18b7a', path: '/etc/nginx/certs' }, ...steady },
  { cluster: 'edge-proxy', name: 'cert-renewer', desired: 1, running: 1, revision: 4, repo: 'cert-renewer', imageTag: '0.2.1', cpu: 256, memory: 512, port: 0, exec: false, status: 'steady', efs: { fileSystemId: 'fs-0a6b33f0', accessPointId: 'fsap-0e44d18b7a', path: '/certs' } },
  { cluster: 'staging-web', name: 'web', desired: 1, running: 1, revision: 33, repo: 'web', imageTag: '3.5.0-rc.1', cpu: 256, memory: 512, port: 3000, lb: true, ...steady },
  { cluster: 'staging-web', name: 'api', desired: 1, running: 1, revision: 21, repo: 'api', imageTag: '1.19.0-rc.2', cpu: 512, memory: 1024, port: 8080, lb: true, ...steady },
]

const clusterDef = (name: string) => CLUSTER_DEFS.find((c) => c.name === name)!
const clusterIdOf = (def: ClusterDef) => clusterId(def.account.account_id, def.region, def.name)
const serviceIdOf = (def: ServiceDef) => {
  const cluster = clusterDef(def.cluster)
  return serviceId(cluster.account.account_id, cluster.region, cluster.name, def.name)
}
const clusterRefOf = (def: ClusterDef) => ({ id: clusterIdOf(def), name: def.name })
const serviceRefOf = (def: ServiceDef) => ({ id: serviceIdOf(def), name: def.name, status: def.status })
const clusterTag = (def: ClusterDef): TagDTO => TAG_BY_NAME[def.tag]
const inheritedTag = (def: ClusterDef): TagDTO => ({ ...clusterTag(def), inherited: true })

// ---- tasks ----

const STANDALONE_RELEASE_BUILD = 'release-build'
const STANDALONE_DB_MIGRATE = 'db-migrate'

function taskBase(
  cluster: ClusterDef,
  seed: string,
  index: number,
): Pick<TaskDTO, 'id' | 'short_id' | 'arn' | 'cluster' | 'account_name' | 'region' | 'az' | 'subnet_id' | 'private_ip'> {
  const arn = `arn:aws:ecs:${cluster.region}:${cluster.account.account_id}:task/${cluster.name}/${hex(seed, 32)}`
  const [az, subnet] = AZ_SUBNETS[cluster.region][index % 2]
  const octet = REGIONS.indexOf(cluster.region) + 1
  return {
    id: taskId(cluster.account.account_id, cluster.region, arn),
    short_id: hex(seed, 8),
    arn,
    cluster: clusterRefOf(cluster),
    account_name: cluster.account.name,
    region: cluster.region,
    az,
    subnet_id: subnet,
    private_ip: `10.${octet}.${index % 2}.${10 + (parseInt(hex(seed, 2), 16) % 200)}`,
  }
}

function serviceTasks(def: ServiceDef): TaskDTO[] {
  const cluster = clusterDef(def.cluster)
  const common = {
    service: serviceRefOf(def),
    group: `service:${def.name}`,
    family: def.name,
    cpu: String(def.cpu),
    memory: String(def.memory),
    capacity: (def.spot ? 'FARGATE_SPOT' : 'FARGATE') as TaskDTO['capacity'],
    exec_enabled: def.exec,
    can: { operate: true, terminal: def.exec },
  }
  const startedBy = (seed: string) => `ecs-svc/${BigInt(`0x${hex(seed, 12)}`).toString().padStart(19, '1').slice(0, 19)}`
  const tasks: TaskDTO[] = []

  for (let i = 0; i < def.running; i++) {
    const rollingNew = def.name === 'notifications' && i === 1
    const unhealthy = def.name === 'catalog-api' && i === 1
    tasks.push({
      ...common,
      ...taskBase(cluster, `${def.name}-run-${i}`, i),
      started_by: startedBy(`${def.name}-sb`),
      status: 'RUNNING',
      exit_code: null,
      health: unhealthy ? 'UNHEALTHY' : 'HEALTHY',
      revision: def.name === 'notifications' ? (rollingNew ? 10 : 9) : def.revision,
      started_at: minutesAgo(rollingNew ? 3 : 4320 + i * 7),
      stopped_at: null,
      stop_code: '',
      stopped_reason: '',
      stop_reason_group: '',
    })
  }

  const stopped = (index: number, fields: Partial<TaskDTO>): TaskDTO => ({
    ...common,
    ...taskBase(cluster, `${def.name}-stop-${index}`, index),
    started_by: startedBy(`${def.name}-sb`),
    status: 'STOPPED',
    exit_code: 0,
    health: 'UNKNOWN',
    revision: def.revision,
    started_at: null,
    stopped_at: null,
    stop_code: 'ServiceSchedulerInitiated',
    stopped_reason: '',
    stop_reason_group: '',
    ...fields,
  })

  if (def.name === 'image-resizer') {
    for (let i = 0; i < 6; i++) {
      const stoppedMinutes = 19 + i * 4
      tasks.push(
        stopped(i, {
          exit_code: 137,
          health: 'UNHEALTHY',
          started_at: minutesAgo(stoppedMinutes + 3),
          stopped_at: minutesAgo(stoppedMinutes),
          stop_code: 'EssentialContainerExited',
          stopped_reason: 'OutOfMemoryError: Container killed due to memory usage',
          stop_reason_group: 'Out of memory (exit 137)',
        }),
      )
    }
  } else if (def.name === 'catalog-api') {
    for (let i = 0; i < 3; i++) {
      const stoppedMinutes = 60 * (1 + i * 2.5)
      tasks.push(
        stopped(i, {
          exit_code: 143,
          health: 'UNHEALTHY',
          started_at: minutesAgo(stoppedMinutes + 180),
          stopped_at: minutesAgo(stoppedMinutes),
          stopped_reason: 'Task failed ELB health checks in (target-group catalog-api-tg)',
          stop_reason_group: 'Failed load balancer health checks',
        }),
      )
    }
  } else if (def.status === 'steady') {
    tasks.push(
      stopped(0, {
        revision: def.revision - 1,
        started_at: minutesAgo(60 * 80),
        stopped_at: minutesAgo(60 * 26),
        stopped_reason: `Scaling activity initiated by (deployment ecs-svc/${startedBy(`${def.name}-old`).slice(8)})`,
        stop_reason_group: 'Scaled in',
      }),
    )
  }
  return tasks
}

function standaloneTasks(): TaskDTO[] {
  const ciTools = clusterDef('ci-tools')
  const background = clusterDef('background-jobs')
  const none = { service: null, can: { operate: true, terminal: false }, exec_enabled: false, health: 'UNKNOWN' as const }
  return [
    {
      ...none,
      ...taskBase(ciTools, 'release-build-0', 0),
      group: `family:${STANDALONE_RELEASE_BUILD}`,
      started_by: 'events-rule/nightly-release',
      status: 'RUNNING',
      exit_code: null,
      family: STANDALONE_RELEASE_BUILD,
      revision: 3,
      capacity: 'FARGATE',
      cpu: '1024',
      memory: '2048',
      started_at: minutesAgo(35),
      stopped_at: null,
      stop_code: '',
      stopped_reason: '',
      stop_reason_group: '',
    },
    {
      ...none,
      ...taskBase(background, 'db-migrate-0', 0),
      group: `family:${STANDALONE_DB_MIGRATE}`,
      started_by: 'pavel',
      status: 'STOPPED',
      exit_code: 0,
      family: STANDALONE_DB_MIGRATE,
      revision: 12,
      capacity: 'FARGATE',
      cpu: '512',
      memory: '1024',
      started_at: minutesAgo(128),
      stopped_at: minutesAgo(120),
      stop_code: 'EssentialContainerExited',
      stopped_reason: 'Essential container in task exited',
      stop_reason_group: '',
    },
  ]
}

/** A failed stop (non-zero exit, or never started) or an unhealthy task: what the API sorts first. */
const isProblem = (task: TaskDTO) =>
  (task.status === 'STOPPED' && ((task.exit_code ?? 0) !== 0 || task.stop_code === 'TaskFailedToStart')) || task.health === 'UNHEALTHY'

/** The API's `problems` filter: everything not STOPPED, plus problems. `running` is also "not STOPPED". */
const inProblemsFilter = (task: TaskDTO) => task.status !== 'STOPPED' || isProblem(task)

const taskSortTime = (task: TaskDTO) => Date.parse(task.stopped_at ?? task.started_at ?? FIXTURE_NOW)

/** Every task in the estate: problems first, then newest first. */
export const ALL_TASKS: TaskDTO[] = [...SERVICE_DEFS.flatMap(serviceTasks), ...standaloneTasks()].sort(
  (a, b) => Number(isProblem(b)) - Number(isProblem(a)) || taskSortTime(b) - taskSortTime(a),
)

const runningTasksOf = (def: ServiceDef) =>
  ALL_TASKS.filter((t) => t.service?.id === serviceIdOf(def) && t.status === 'RUNNING')

function stopReasons(items: TaskDTO[]): ReasonCountDTO[] {
  const counts = new Map<string, number>()
  for (const task of items) {
    if (task.status === 'STOPPED' && task.stop_reason_group) {
      counts.set(task.stop_reason_group, (counts.get(task.stop_reason_group) ?? 0) + 1)
    }
  }
  return [...counts].map(([reason, count]) => ({ reason, count })).sort((a, b) => b.count - a.count)
}

/** Builds a TasksListDTO whose counts and summary are derived from `items`. */
export function tasksList(items: TaskDTO[]): TasksListDTO {
  const running = items.filter((t) => t.status !== 'STOPPED')
  const stopped = items.filter((t) => t.status === 'STOPPED')
  const azCounts = new Map<string, number>()
  for (const task of running) azCounts.set(task.az, (azCounts.get(task.az) ?? 0) + 1)
  const clusters = new Map<string, { id: string; name: string; fargate: number; spot: number }>()
  for (const task of running) {
    const row = clusters.get(task.cluster.id) ?? { id: task.cluster.id, name: task.cluster.name, fargate: 0, spot: 0 }
    if (task.capacity === 'FARGATE_SPOT') row.spot++
    else row.fargate++
    clusters.set(task.cluster.id, row)
  }
  return {
    items,
    next: null,
    polled_at: FIXTURE_NOW,
    counts: {
      problems: items.filter(inProblemsFilter).length,
      running: running.length,
      stopped: stopped.length,
      all: items.length,
      stopped_error: stopped.filter(isProblem).length,
    },
    summary: {
      by_status: {
        running_healthy: running.filter((t) => t.health !== 'UNHEALTHY').length,
        running_unhealthy: running.filter((t) => t.health === 'UNHEALTHY').length,
        stopped_error: stopped.filter(isProblem).length,
        stopped_normal: stopped.filter((t) => !isProblem(t)).length,
      },
      by_az: [...azCounts].map(([az, count]) => ({ az, count })).sort((a, b) => a.az.localeCompare(b.az)),
      by_cluster: [...clusters.values()],
      stop_reasons: stopReasons(items),
    },
  }
}

/** Applies the API's `?filter=` to a task list (anything but problems|running|stopped means all). */
export function filterTasks(items: TaskDTO[], filter: string | null): TaskDTO[] {
  if (filter === 'problems') return items.filter(inProblemsFilter)
  if (filter === 'running') return items.filter((t) => t.status !== 'STOPPED')
  if (filter === 'stopped') return items.filter((t) => t.status === 'STOPPED')
  return items
}

/** The list the API returns for `/api/tasks?filter=&q=`: items are filtered, counts are for the filtered scope. */
export function tasksResponse(filter: string | null, q: string | null): TasksListDTO {
  const needle = (q ?? '').toLowerCase()
  const matchesQuery = (t: TaskDTO) =>
    !needle || [t.short_id, t.family, t.service?.name ?? '', t.cluster.name, t.private_ip].some((v) => v.toLowerCase().includes(needle))
  const scoped = ALL_TASKS.filter(matchesQuery)
  const body = tasksList(scoped)
  const items = filterTasks(scoped, filter)
  return { ...body, items }
}

export const TASKS: TasksListDTO = tasksList(ALL_TASKS)

// ---- time series ----

function history(running: number, desired: number): PointDTO[] {
  return Array.from({ length: 72 }, (_, i) => {
    const dip = i >= 40 && i < 44 ? 1 : 0
    return { t: minutesAgo((71 - i) * 60), running: Math.max(0, running - dip), desired }
  })
}

function stopsPerHour(normalBase: number, errorHours: number[]): StopsHourDTO[] {
  return Array.from({ length: 24 }, (_, i) => ({
    hour: minutesAgo((23 - i) * 60),
    normal: (i + normalBase) % 3,
    error: errorHours.includes(i) ? 2 : 0,
  }))
}

function deploymentsPerDay(): DeploysDayDTO[] {
  return Array.from({ length: 14 }, (_, i) => ({
    day: minutesAgo((13 - i) * 1440).slice(0, 10),
    deploy: i % 4 === 0 ? 2 : i % 3,
    restart: i === 9 ? 1 : 0,
    failed: i === 11 ? 1 : 0,
  }))
}

// ---- events ----

function serviceEvents(def: ServiceDef): EventDTO[] {
  const cluster = clusterDef(def.cluster)
  const base = { service: serviceRefOf(def), cluster: clusterRefOf(cluster) }
  const ecs = (minutes: number, text: string): EventDTO => ({ ts: minutesAgo(minutes), source: 'ecs', actor: 'ECS', ...base, text })
  const events: EventDTO[] = [
    ecs(60 * 30, `(service ${def.name}) has reached a steady state.`),
    { ts: minutesAgo(60 * 26), source: 'harbormaster', actor: 'pavel', ...base, text: `Restarted service (force new deployment) on revision ${def.revision}.` },
    ecs(60 * 26 - 6, `(service ${def.name}) has reached a steady state.`),
  ]
  if (def.lb) events.push(ecs(60 * 26 - 3, `(service ${def.name}) registered 1 targets in (target-group ${def.name}-tg).`))
  if (def.name === 'image-resizer') {
    events.push(
      ecs(25, `(service ${def.name}) has started 2 tasks: (task ${hex('ir-a', 8)}) (task ${hex('ir-b', 8)}).`),
      ecs(19, `(service ${def.name}) has stopped 2 running tasks: (task ${hex('ir-a', 8)}) (task ${hex('ir-b', 8)}). Reason: OutOfMemoryError.`),
      ecs(18, `(service ${def.name}) is unable to consistently start tasks successfully.`),
    )
  }
  if (def.name === 'catalog-api') {
    events.push(ecs(42, `(service ${def.name}) (port ${def.port}) is unhealthy in (target-group ${def.name}-tg) due to (reason Health checks failed with these codes: [503]).`))
  }
  if (def.name === 'notifications') {
    events.push(ecs(3, `(service ${def.name}) has started 1 tasks: (task ${hex('notif-new', 8)}).`))
  }
  return events.sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts))
}

const eventsOf = (def: ServiceDef) => serviceEvents(def)
const ALL_EVENTS: EventDTO[] = SERVICE_DEFS.flatMap(eventsOf).sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts))

// ---- builders: slots, clusters, services ----

function slotsOf(def: ServiceDef): SlotDTO[] {
  return runningTasksOf(def).map((t) => ({
    task_id: t.id,
    short_id: t.short_id,
    private_ip: t.private_ip,
    health: t.health,
    rolling_out: def.name === 'notifications' && t.revision === 10,
  }))
}

function clusterCard(def: ClusterDef): ClusterCardDTO {
  const services = SERVICE_DEFS.filter((s) => s.cluster === def.name)
  const running = ALL_TASKS.filter((t) => t.cluster.id === clusterIdOf(def) && t.status === 'RUNNING')
  const changes = services.map((s) => (s.status === 'steady' ? 60 * 26 : 3))
  return {
    id: clusterIdOf(def),
    name: def.name,
    tag: clusterTag(def),
    status: def.status,
    account_id: def.account.account_id,
    account_name: def.account.name,
    region: def.region,
    services: services.map(serviceRefOf),
    running: services.reduce((sum, s) => sum + s.running, 0),
    desired: services.reduce((sum, s) => sum + s.desired, 0),
    reserved_vcpu: running.reduce((sum, t) => sum + Number(t.cpu), 0) / 1024,
    reserved_gib: running.reduce((sum, t) => sum + Number(t.memory), 0) / 1024,
    spot_tasks: running.filter((t) => t.capacity === 'FARGATE_SPOT').length,
    container_insights: def.insights,
    last_change_at: minutesAgo(Math.min(...changes)),
  }
}

function clusterDetail(def: ClusterDef): ClusterDTO {
  const card = clusterCard(def)
  const running = ALL_TASKS.filter((t) => t.cluster.id === card.id && t.status === 'RUNNING')
  const services: ClusterServiceDTO[] = SERVICE_DEFS.filter((s) => s.cluster === def.name).map((s) => ({
    id: serviceIdOf(s),
    name: s.name,
    tag: inheritedTag(def),
    status: s.status,
    family: s.name,
    revision: s.revision,
    image_tag: s.imageTag,
    running: s.running,
    desired: s.desired,
    cpu: String(s.cpu),
    memory: String(s.memory),
    slots: slotsOf(s),
    latest_registered_revision: null,
    last_change_at: minutesAgo(s.status === 'steady' ? 60 * 26 : 3),
  }))
  return {
    ...card,
    arn: `arn:aws:ecs:${def.region}:${def.account.account_id}:cluster/${def.name}`,
    console_url: `https://${def.region}.console.aws.amazon.com/ecs/v2/clusters/${def.name}`,
    fargate_tasks: running.filter((t) => t.capacity === 'FARGATE').length,
    tasks_history_72h: history(card.running, card.desired),
    stops_per_hour: stopsPerHour(def.name.length, def.status === 'needs_attention' || def.status === 'degraded' ? [20, 21, 22] : []),
    services_detail: services,
    vpc: { vpc_id: 'vpc-0a41c7e29b3d', name: 'homelab-main' },
    polled_at: FIXTURE_NOW,
  }
}

function serviceDetail(def: ServiceDef): ServiceDTO {
  const cluster = clusterDef(def.cluster)
  const running = runningTasksOf(def)
  const registry = `${cluster.account.account_id}.dkr.ecr.${cluster.region}.amazonaws.com`
  const tdArn = (revision: number) => `arn:aws:ecs:${cluster.region}:${cluster.account.account_id}:task-definition/${def.name}:${revision}`
  const notifications = def.name === 'notifications'
  const failing = def.status === 'failing'
  const deployment = (status: 'PRIMARY' | 'ACTIVE', revision: number, running: number, state: 'IN_PROGRESS' | 'COMPLETED' | 'FAILED') => ({
    id: `ecs-svc/${BigInt(`0x${hex(`${def.name}-dep-${revision}`, 12)}`).toString().padStart(19, '1').slice(0, 19)}`,
    status,
    revision,
    desired_count: def.desired,
    running_count: running,
    pending_count: 0,
    failed_tasks: state === 'FAILED' ? 6 : 0,
    rollout_state: state,
    rollout_state_reason:
      state === 'COMPLETED' ? 'ECS deployment completed successfully.'
      : state === 'FAILED' ? 'ECS deployment circuit breaker: tasks failed to start.'
      : 'ECS deployment in progress.',
    created_at: minutesAgo(status === 'PRIMARY' && notifications ? 4 : 60 * 80),
    updated_at: minutesAgo(status === 'PRIMARY' && notifications ? 3 : 60 * 78),
  })
  const subnetCounts = new Map<string, number>()
  for (const task of running) subnetCounts.set(task.subnet_id, (subnetCounts.get(task.subnet_id) ?? 0) + 1)
  const subnets = (subnetCounts.size ? [...subnetCounts] : [[AZ_SUBNETS[cluster.region][0][1], 0] as [string, number]]).map(([id, tasks]) => ({ id, tasks }))
  const containers = [{ name: def.name, image: `${registry}/${def.repo}:${def.imageTag}`, essential: true }]
  if (def.name === 'payments-api') {
    containers.push({ name: 'otel-collector', image: 'public.ecr.aws/aws-observability/aws-otel-collector:v0.40.0', essential: false })
  }

  return {
    id: serviceIdOf(def),
    name: def.name,
    cluster: clusterRefOf(cluster),
    account_id: cluster.account.account_id,
    account_name: cluster.account.name,
    region: cluster.region,
    tag: inheritedTag(cluster),
    status: def.status,
    family: def.name,
    revision: def.revision,
    image_tag: def.imageTag,
    task_definition_arn: tdArn(def.revision),
    launch_type: 'FARGATE',
    platform_version: 'LATEST',
    deployment_controller: 'ECS',
    desired: def.desired,
    running: def.running,
    pending: 0,
    enable_execute_command: def.exec,
    min_healthy_percent: 100,
    max_percent: 200,
    circuit_breaker: { enable: true, rollback: def.name === 'payments-api' },
    deployments: notifications
      ? [deployment('PRIMARY', 10, 1, 'IN_PROGRESS'), deployment('ACTIVE', 9, 1, 'COMPLETED')]
      : [deployment('PRIMARY', def.revision, def.running, failing ? 'FAILED' : 'COMPLETED')],
    running_tasks: running.map((t) => ({
      id: t.id,
      short_id: t.short_id,
      health: t.health,
      az: t.az,
      subnet_id: t.subnet_id,
      private_ip: t.private_ip,
      revision: t.revision,
      spot: t.capacity === 'FARGATE_SPOT',
      exec_agent_running: t.exec_enabled,
    })),
    slots: slotsOf(def),
    networking: {
      subnets,
      vpc: { vpc_id: 'vpc-0a41c7e29b3d', name: 'homelab-main' },
      security_group_names: { [`sg-0${hex(`${def.name}-sg`, 8)}`]: def.name },
      security_groups: [`sg-0${hex(`${def.name}-sg`, 8)}`],
      assign_public_ip: def.name === 'edge-nginx',
      load_balancers: def.lb ? [{ target_group_name: `${def.name}-tg`, container_name: def.name, container_port: def.port }] : [],
    },
    task_definition: {
      family: def.name,
      revision: def.revision,
      cpu: String(def.cpu),
      memory: String(def.memory),
      containers,
      task_role_arn: `arn:aws:iam::${cluster.account.account_id}:role/${def.name}-task`,
      efs_mounts: def.efs ? [{ file_system_id: def.efs.fileSystemId, access_point_id: def.efs.accessPointId, container_path: def.efs.path }] : [],
    },
    counts_history_72h: history(def.running, def.desired),
    deployments_14d: [
      { deployment_id: `ecs-svc/${hex(`${def.name}-d1`, 12)}`, revision: def.revision, kind: 'deploy', outcome: notifications ? '' : 'succeeded', started_at: minutesAgo(notifications ? 4 : 60 * 80), finished_at: notifications ? null : minutesAgo(60 * 78) },
      { deployment_id: `ecs-svc/${hex(`${def.name}-d2`, 12)}`, revision: def.revision - 1, kind: 'restart', outcome: 'succeeded', started_at: minutesAgo(60 * 26), finished_at: minutesAgo(60 * 26 - 6) },
    ],
    recent_events: eventsOf(def).slice(0, 6),
    can: { view: true, logs: true, operate: true, deploy: true, edit: true, terminal: true },
    console_url: `https://${cluster.region}.console.aws.amazon.com/ecs/v2/clusters/${cluster.name}/services/${def.name}`,
    polled_at: FIXTURE_NOW,
    controller: { type: 'ECS', supported: true, name: '', banner: '' },
    strategy_text: 'Rolling update · min healthy 100% · max 200% · circuit breaker with rollback is on',
  }
}

const serviceDef = (name: string) => SERVICE_DEFS.find((s) => s.name === name)!

/** Every cluster detail and service detail, keyed by API id, for the MSW detail handlers. */
export const CLUSTER_DETAILS: Record<string, ClusterDTO> = Object.fromEntries(CLUSTER_DEFS.map((c) => [clusterIdOf(c), clusterDetail(c)]))
export const SERVICE_DETAILS: Record<string, ServiceDTO> = Object.fromEntries(SERVICE_DEFS.map((s) => [serviceIdOf(s), serviceDetail(s)]))

export const CLUSTER_CORE: ClusterDTO = CLUSTER_DETAILS[clusterIdOf(clusterDef('core-services'))]
export const SERVICE_PAYMENTS: ServiceDTO = SERVICE_DETAILS[serviceIdOf(serviceDef('payments-api'))]
export const SERVICE_NOTIFICATIONS: ServiceDTO = SERVICE_DETAILS[serviceIdOf(serviceDef('notifications'))]

export const CLUSTERS: ListDTO<ClusterCardDTO> = { items: CLUSTER_DEFS.map(clusterCard), next: null, polled_at: FIXTURE_NOW }

export const OPERATIONS_FOR = (serviceIdValue: string): EventDTO[] =>
  ALL_EVENTS.filter((e) => e.service.id === serviceIdValue)

// ---- overview ----

const byStatus = SERVICE_DEFS.reduce<Partial<Record<Status, number>>>((acc, s) => ({ ...acc, [s.status]: (acc[s.status] ?? 0) + 1 }), {})

export const OVERVIEW: OverviewDTO = {
  polled_at: FIXTURE_NOW,
  accounts: 2,
  regions: 3,
  clusters: CLUSTER_DEFS.map((c) => ({ id: clusterIdOf(c), name: c.name, status: c.status })),
  services: { total: SERVICE_DEFS.length, by_status: byStatus },
  tasks: {
    running: SERVICE_DEFS.reduce((n, s) => n + s.running, 0),
    desired: SERVICE_DEFS.reduce((n, s) => n + s.desired, 0),
    pending: 0,
    unhealthy: 1,
    history_72h: history(21, 23),
  },
  task_health: { healthy: 19, unhealthy: 1, rolling_out: 1, missing: 2 },
  per_cluster: CLUSTERS.items.map((c) => ({
    id: c.id,
    name: c.name,
    running: c.running,
    desired: c.desired,
    unhealthy: SERVICE_DEFS.filter((s) => s.cluster === c.name && s.status === 'unhealthy_task').length,
    rolling_out: c.name === 'core-services' ? 1 : 0,
    missing: Math.max(c.desired - c.running, 0),
  })),
  stops_per_hour: stopsPerHour(1, [20, 21, 22]),
  deployments_per_day: deploymentsPerDay(),
  stop_reasons: stopReasons(ALL_TASKS.filter((t) => t.stopped_at && Date.parse(t.stopped_at) > NOW_MS - 3 * 86_400_000)),
  events: ALL_EVENTS.slice(0, 10),
}

// ---- users, permissions, tags ----

const ME_TAGS = (visible: TagDTO[]) =>
  visible.map((t) => ({ id: t.id, name: t.name, color: t.color, builtin: t.id === 1, services_in_effect: servicesInEffect(t.id) }))

function servicesInEffect(tagId: number): number {
  return SERVICE_DEFS.filter((s) => clusterTag(clusterDef(s.cluster)).id === tagId).length
}

const ME_ACCOUNTS = [
  { ...ACCOUNT_HOMELAB, regions: REGIONS },
  { ...ACCOUNT_STAGING, regions: ['us-east-1'] },
]

export const ME_ADMIN: MeDTO = {
  id: 1,
  username: 'pavel',
  admin: true,
  must_change_password: false,
  csrf_token: 'csrf-token-pavel',
  perms: {},
  tags: ME_TAGS([TAG_DEV, TAG_STAGING, TAG_PROD]),
  accounts: ME_ACCOUNTS,
  regions: REGIONS,
  has_password: true,
  sso_linked: false,
  break_glass: true,
  email: 'pavel@example.test',
  display_name: 'Pavel',
}

export const ME_OMER: MeDTO = {
  id: 3,
  username: 'omer',
  admin: false,
  must_change_password: false,
  csrf_token: 'csrf-token-omer',
  perms: { '1': 7, '2': 1 },
  tags: ME_TAGS([TAG_DEV, TAG_STAGING]),
  accounts: ME_ACCOUNTS,
  regions: REGIONS,
  has_password: true,
  sso_linked: false,
  break_glass: false,
  email: null,
  display_name: null,
}

/** A user who signs in only through SSO: linked identity, no password. */
export const ME_SSO_USER: MeDTO = {
  id: 5,
  username: 'alex',
  admin: false,
  must_change_password: false,
  csrf_token: 'csrf-token-alex',
  perms: { '1': 1 },
  tags: ME_TAGS([TAG_DEV]),
  accounts: ME_ACCOUNTS,
  regions: REGIONS,
  has_password: false,
  sso_linked: true,
  break_glass: false,
  email: 'alex@example.test',
  display_name: 'Alex',
}

const plainTag = (t: TagDTO): TagDTO => ({ ...t, inherited: false })

/** Credential fields every user carries; each user below overrides what differs. */
const NO_SSO = {
  has_password: true,
  break_glass: false,
  sso: 'none' as const,
  sso_name_id: null,
  pending_link_expires_at: null,
  email: null,
  display_name: null,
}

export const USERS: UsersDTO = {
  items: [
    {
      id: 1,
      username: 'pavel',
      admin: true,
      must_change_password: false,
      last_sign_in_at: minutesAgo(2),
      can_see: [TAG_DEV, TAG_STAGING, TAG_PROD].map(plainTag),
      ...NO_SSO,
      break_glass: true,
      sso: 'linked',
      sso_name_id: 'pavel@example.test',
      email: 'pavel@example.test',
      display_name: 'Pavel',
    },
    {
      id: 2,
      username: 'dana',
      admin: false,
      must_change_password: false,
      last_sign_in_at: minutesAgo(50),
      can_see: [TAG_DEV, TAG_STAGING, TAG_PROD].map(plainTag),
      ...NO_SSO,
      sso: 'pending',
      sso_name_id: 'dana@example.test',
      pending_link_expires_at: minutesAgo(-14 * 1440),
      email: 'dana@example.test',
      display_name: 'Dana',
    },
    {
      id: 3,
      username: 'omer',
      admin: false,
      must_change_password: false,
      last_sign_in_at: minutesAgo(26 * 60),
      can_see: [TAG_DEV, TAG_STAGING].map(plainTag),
      ...NO_SSO,
    },
    {
      id: 4,
      username: 'oncall',
      admin: false,
      must_change_password: false,
      last_sign_in_at: minutesAgo(3 * 1440),
      can_see: [TAG_STAGING, TAG_PROD].map(plainTag),
      ...NO_SSO,
      sso: 'pending_expired',
      sso_name_id: 'oncall@example.test',
      pending_link_expires_at: minutesAgo(2 * 1440),
    },
    {
      id: 5,
      username: 'alex',
      admin: false,
      must_change_password: false,
      last_sign_in_at: minutesAgo(5 * 60),
      can_see: [TAG_DEV].map(plainTag),
      has_password: false,
      break_glass: false,
      sso: 'linked',
      sso_name_id: 'alex@example.test',
      pending_link_expires_at: null,
      email: 'alex@example.test',
      display_name: 'Alex',
    },
  ],
}

export const PERMISSIONS: PermissionsDTO = {
  tags: [TAG_DEV, TAG_STAGING, TAG_PROD].map((t) => ({ id: t.id, name: t.name, color: t.color, services_in_effect: servicesInEffect(t.id) })),
  users: [
    { id: 1, username: 'pavel', admin: true, perms: {} },
    { id: 2, username: 'dana', admin: false, perms: { '1': 63, '2': 31, '3': 3 } },
    { id: 3, username: 'omer', admin: false, perms: { '1': 7, '2': 1 } },
    { id: 4, username: 'oncall', admin: false, perms: { '2': 39, '3': 39 } },
  ],
}

export const TAGS: TagsDTO = {
  items: [
    { id: 1, name: 'Dev', color: 'neutral', builtin: true, clusters: 4, services_direct: 0, services_effective: servicesInEffect(1), users: [{ username: 'dana', preset: 'Full' }, { username: 'omer', preset: 'Operator' }] },
    { id: 2, name: 'Staging', color: 'amber', builtin: false, clusters: 1, services_direct: 0, services_effective: servicesInEffect(2), users: [{ username: 'dana', preset: 'Deployer' }, { username: 'omer', preset: 'Viewer' }, { username: 'oncall', preset: 'Custom' }] },
    { id: 3, name: 'Prod', color: 'red', builtin: false, clusters: 1, services_direct: 0, services_effective: servicesInEffect(3), users: [{ username: 'dana', preset: 'Custom' }, { username: 'oncall', preset: 'Custom' }] },
  ],
}

export const ASSIGNMENTS: TagAssignmentsDTO = {
  clusters: CLUSTER_DEFS.map((c) => ({
    id: clusterIdOf(c),
    name: c.name,
    account_name: c.account.name,
    region: c.region,
    tag_id: c.tag === 'Dev' ? null : clusterTag(c).id,
    effective: clusterTag(c),
    services: SERVICE_DEFS.filter((s) => s.cluster === c.name).map((s) => ({
      id: serviceIdOf(s),
      name: s.name,
      tag_id: null,
      effective: inheritedTag(c),
    })),
  })),
}

// ---- accounts, audit, poller ----

export const ACCOUNTS: AccountsDTO = {
  items: [ACCOUNT_HOMELAB, ACCOUNT_STAGING].map((account, index) => ({
    id: account.id,
    name: account.name,
    account_id: account.account_id,
    role_arn: `arn:aws:iam::${account.account_id}:role/harbormaster-access`,
    external_id: hex(`external-${account.account_id}`, 24),
    regions: index === 0 ? REGIONS : ['us-east-1'],
    enabled: true,
    clusters: CLUSTER_DEFS.filter((c) => c.account === account).length,
    status: 'connected' as const,
    last_ok_at: minutesAgo(0),
    last_error: '',
  })),
  runtime: {
    cluster: 'core-services',
    service: 'harbormaster',
    intervals: { services: '5s', running_tasks: '5s', full_tasks: '30s', stopped_tasks: '60s', clusters: '60s', list_services: '60s', counts_history: '60s' },
  },
}

export const AUDIT: AuditDTO = {
  items: [
    { id: 5, ts: minutesAgo(2), actor: 'pavel', action: 'auth.login', target: 'pavel', summary: 'Signed in.', details: null, ip: '10.0.4.17' },
    { id: 4, ts: minutesAgo(180), actor: 'pavel', action: 'permission.set', target: 'oncall', summary: 'Gave oncall Terminal on Prod.', details: { tag: 'Prod', actions: 39 }, ip: '10.0.4.17' },
    { id: 3, ts: minutesAgo(300), actor: 'pavel', action: 'cluster.tag', target: 'staging-web', summary: 'Tagged cluster staging-web as Staging (was Dev).', details: { from: 'Dev', to: 'Staging' }, ip: '10.0.4.17' },
    { id: 2, ts: minutesAgo(2 * 1440), actor: 'pavel', action: 'user.create', target: 'omer', summary: 'Created user omer with Operator on Dev.', details: null, ip: '10.0.4.17' },
    { id: 1, ts: minutesAgo(6 * 1440), actor: 'pavel', action: 'tag.create', target: 'Prod', summary: 'Created tag Prod.', details: null, ip: '10.0.4.17' },
  ],
  next: null,
  polled_at: FIXTURE_NOW,
}

export const POLLER: PollerDTO = {
  items: [
    ...REGIONS.map((region) => ({ account_id: ACCOUNT_HOMELAB.account_id, account_name: ACCOUNT_HOMELAB.name, region })),
    { account_id: ACCOUNT_STAGING.account_id, account_name: ACCOUNT_STAGING.name, region: 'us-east-1' },
  ].map((entry) => ({ ...entry, throttled: false, failing: false, last_error: '', last_ok_at: minutesAgo(0) })),
}

// ---- observe (plan 03): metrics, target health, scaling, alerts, log history ----

const METRIC_POINTS = 12
const METRIC_STEP_MIN = 30
const metricTimestamps = Array.from({ length: METRIC_POINTS }, (_, i) => minutesAgo((METRIC_POINTS - 1 - i) * METRIC_STEP_MIN))

/** A smooth deterministic series around `base`, with optional gaps (indexes that CloudWatch had no datapoint for). */
function wave(base: number, amplitude: number, phase: number, gaps: number[] = []): Series {
  return Array.from({ length: METRIC_POINTS }, (_, i) =>
    gaps.includes(i) ? null : Math.round((base + amplitude * Math.sin(i / 2 + phase)) * 10) / 10,
  )
}

const lastPoint = (series: Series): number | null => series[series.length - 1] ?? null

const paymentsId = serviceIdOf(serviceDef('payments-api'))
const catalogId = serviceIdOf(serviceDef('catalog-api'))
const edgeNginxId = serviceIdOf(serviceDef('edge-nginx'))
const imageResizerId = serviceIdOf(serviceDef('image-resizer'))
const backgroundJobsClusterId = clusterIdOf(clusterDef('background-jobs'))
const edgeProxyClusterId = clusterIdOf(clusterDef('edge-proxy'))
const euAppsClusterId = clusterIdOf(clusterDef('eu-apps'))
const coreClusterId = clusterIdOf(clusterDef('core-services'))

const paymentsCpu = wave(34, 10, 0)
const paymentsMem = wave(58, 6, 1)

/** payments-api, 6 h of Container Insights data: ALB, three running tasks and one deploy marker. */
export const SERVICE_METRICS_PAYMENTS: ServiceMetricsDTO = {
  range: '6h',
  period: 300,
  insights: 'enhanced',
  timestamps: metricTimestamps,
  series: { cpu: paymentsCpu, mem: paymentsMem, net_rx_kbps: wave(220, 60, 2), net_tx_kbps: wave(340, 80, 3), tasks: wave(3, 0, 0) },
  now: { cpu: lastPoint(paymentsCpu), mem: lastPoint(paymentsMem), cpu_units: 512, mem_mib: 1024, running: 3 },
  alb: {
    req_per_min: wave(420, 90, 0.5),
    p95_ms: wave(180, 40, 1.5),
    err5xx_per_hour: { timestamps: metricTimestamps.slice(-6), values: [0, 0, 2, 0, 1, 0] },
    req_now: 438,
    p95_now: 171.2,
    err5xx_today: 3,
  },
  tasks_now: [
    { task_id: 'a1b2c3d4e5f60718', cpu: 31.2, mem: 57.4 },
    { task_id: 'b2c3d4e5f6071829', cpu: 36.8, mem: 59.1 },
    { task_id: 'c3d4e5f607182930', cpu: null, mem: null },
  ],
  markers: [{ at: minutesAgo(150), label: 'deploy rev 42', kind: 'deploy', failed: false }],
  stale: null,
  polled_at: FIXTURE_NOW,
}

/** catalog-api with Container Insights off: no series, no usage numbers, but ALB data that has a gap (missing datapoints are null). */
export const SERVICE_METRICS_CATALOG_NO_INSIGHTS: ServiceMetricsDTO = {
  range: '24h',
  period: 900,
  insights: 'disabled',
  timestamps: metricTimestamps,
  series: null,
  now: null,
  alb: {
    req_per_min: wave(95, 20, 0.2, [3, 4]),
    p95_ms: wave(310, 70, 0.9, [3, 4]),
    err5xx_per_hour: { timestamps: metricTimestamps.slice(-6), values: [0, 4, 9, 6, 2, 1] },
    req_now: 98,
    p95_now: 322.5,
    err5xx_today: 22,
  },
  tasks_now: null,
  markers: [],
  stale: null,
  polled_at: FIXTURE_NOW,
}

export const SERVICE_METRICS_STALE: ServiceMetricsDTO = {
  ...SERVICE_METRICS_PAYMENTS,
  stale: { reason: 'CloudWatch is throttling requests', fetched_at: minutesAgo(12) },
}

const clusterCpu = wave(41, 8, 0.3)
const clusterMem = wave(63, 5, 1.2)

export const CLUSTER_METRICS_CORE: ClusterMetricsDTO = {
  range: '24h',
  period: 1800,
  insights: 'enhanced',
  timestamps: metricTimestamps,
  cluster: { cpu: clusterCpu, mem: clusterMem },
  now: { cpu: lastPoint(clusterCpu), mem: lastPoint(clusterMem) },
  markers: [{ at: minutesAgo(150), label: ':42', kind: 'deploy', failed: false, service: 'payments-api' }],
  services: {
    [paymentsId]: {
      cpu: paymentsCpu,
      mem: paymentsMem,
      cpu_now: lastPoint(paymentsCpu),
      mem_now: lastPoint(paymentsMem),
      alb: { req_now: 438, p95_now: 171.2, err5xx_today: 3 },
    },
    [serviceIdOf(serviceDef('notifications'))]: {
      cpu: wave(18, 4, 2),
      mem: wave(44, 3, 0),
      cpu_now: 17.4,
      mem_now: 44.9,
      alb: null,
    },
  },
  stale: null,
}

export const CLUSTERS_METRICS: ClustersMetricsDTO = {
  items: {
    [coreClusterId]: { insights: 'enhanced', cpu: clusterCpu, mem: clusterMem, cpu_now: lastPoint(clusterCpu), mem_now: lastPoint(clusterMem) },
    [euAppsClusterId]: { insights: 'enabled', cpu: wave(52, 9, 1), mem: wave(71, 4, 2), cpu_now: 49.3, mem_now: 72.8 },
    [edgeProxyClusterId]: { insights: 'disabled', cpu: [], mem: [], cpu_now: null, mem_now: null },
  },
  stale: null,
  errors: [],
}

export const OVERVIEW_METRICS: OverviewMetricsDTO = {
  timestamps: metricTimestamps,
  cpu_by_cluster: [
    { id: coreClusterId, label: 'core-services', values: clusterCpu },
    { id: euAppsClusterId, label: 'eu-apps', values: wave(52, 9, 1, [0]) },
  ],
  mem_by_cluster: [
    { id: coreClusterId, label: 'core-services', values: clusterMem },
    { id: euAppsClusterId, label: 'eu-apps', values: wave(71, 4, 2, [0]) },
  ],
  avg: { cpu: 45.1, mem: 67.9 },
  stale: null,
  errors: [],
}

/** edge-nginx: two targets in one group, one of them failing its health check. */
export const TARGET_HEALTH_EDGE: TargetHealthDTO = {
  groups: [
    {
      target_group_arn: `arn:aws:elasticloadbalancing:eu-west-1:${ACCOUNT_HOMELAB.account_id}:targetgroup/edge-nginx/${hex('edge-nginx-tg', 16)}`,
      name: 'edge-nginx',
      healthy: 1,
      unhealthy: 1,
      targets: [
        { id: '10.0.2.41', port: 443, az: 'eu-west-1a', state: 'healthy', reason: '', description: '' },
        { id: '10.0.3.17', port: 443, az: 'eu-west-1b', state: 'unhealthy', reason: 'Target.FailedHealthChecks', description: 'Health checks failed' },
      ],
    },
  ],
  healthy: 1,
  unhealthy: 1,
  polled_at: FIXTURE_NOW,
}

export const SCALING_PAYMENTS: ScalingDTO = {
  has_target: true,
  min: 2,
  max: 8,
  policies: [
    { name: 'cpu-target-60', type: 'TargetTrackingScaling', metric: 'ECSServiceAverageCPUUtilization', target_value: 60, scale_out_cooldown: 60, scale_in_cooldown: 300 },
  ],
  scheduled_actions: [{ name: 'business-hours', schedule: 'cron(0 7 * * ? *)', timezone: 'Asia/Jerusalem', min_capacity: 3, max_capacity: null }],
  activities: [
    {
      id: 'act-4f1c0e2a',
      description: 'Setting desired count to 3.',
      cause: 'monitor alarm TargetTracking-service/core-services/payments-api-AlarmHigh in state ALARM triggered policy cpu-target-60',
      status_code: 'Successful',
      status_message: 'Successfully set desired count to 3.',
      started_at: minutesAgo(95),
      ended_at: minutesAgo(94),
    },
    {
      id: 'act-77d9b315',
      description: 'Setting desired count to 4.',
      cause: 'monitor alarm TargetTracking-service/core-services/payments-api-AlarmHigh in state ALARM triggered policy cpu-target-60',
      status_code: 'InProgress',
      status_message: '',
      started_at: minutesAgo(1),
      ended_at: null,
    },
  ],
  polled_at: FIXTURE_NOW,
}

/** A service without a scaling target: the Scaling tab shows its empty state. */
export const SCALING_NONE: ScalingDTO = {
  has_target: false,
  min: 0,
  max: 0,
  policies: [],
  scheduled_actions: [],
  activities: [],
  polled_at: FIXTURE_NOW,
}

export const ALERTS: AlertsDTO = {
  items: [
    {
      kind: 'service_failing',
      severity: 'danger',
      title: 'image-resizer: 0 of 2 tasks running',
      text: 'No task is running; ECS keeps failing to start one.',
      at: minutesAgo(6),
      service_id: imageResizerId,
      service: 'image-resizer',
      cluster: 'background-jobs',
      cluster_id: backgroundJobsClusterId,
      tab: 'tasks',
    },
    {
      kind: 'account_unreachable',
      severity: 'danger',
      title: 'homelab-admin / eu-west-1: unreachable',
      text: 'AccessDenied: the role could not be assumed.',
      at: minutesAgo(3),
      service_id: '',
      service: '',
      cluster: '',
      cluster_id: '',
      tab: '',
    },
    {
      kind: 'unhealthy_task',
      severity: 'warning',
      title: 'catalog-api: 1 unhealthy task',
      text: 'One task is failing its health check.',
      at: minutesAgo(48),
      service_id: catalogId,
      service: 'catalog-api',
      cluster: 'eu-apps',
      cluster_id: euAppsClusterId,
      tab: 'tasks',
    },
    {
      kind: 'rollout_in_progress',
      severity: 'info',
      title: 'edge-nginx: rollout in progress',
      text: 'Deploying revision 15, 1 of 2 tasks replaced.',
      at: minutesAgo(9),
      service_id: edgeNginxId,
      service: 'edge-nginx',
      cluster: 'edge-proxy',
      cluster_id: edgeProxyClusterId,
      tab: 'overview',
    },
  ],
  counts: { danger: 2, warning: 1, info: 1 },
  polled_at: FIXTURE_NOW,
}

export const LOGS_HISTORY_PAYMENTS: LogsHistoryDTO = {
  group: '/ecs/payments-api',
  region: 'eu-west-1',
  containers: [
    { name: 'app', driver: 'awslogs', supported: true, stream_prefix: 'ecs' },
    { name: 'datadog-agent', driver: 'awslogs', supported: false, reason: 'other_group' },
    { name: 'log-router', driver: 'awsfirelens', supported: false, reason: 'driver' },
  ],
  lines: [
    { t: minutesAgo(5), container: 'app', task_id: 'a1b2c3d4e5f60718', message: 'POST /v1/charges 201 38ms' },
    { t: minutesAgo(4), container: 'app', task_id: 'b2c3d4e5f6071829', message: 'GET /healthz 200 1ms' },
    { t: minutesAgo(3), container: 'app', task_id: 'a1b2c3d4e5f60718', message: 'ERROR charge declined: card_expired' },
  ],
  older_before: minutesAgo(5),
  truncated: false,
}

// ---- Change-path reads (plan 02) ----
export const TASK_DEFINITION_PAYMENTS: TaskDefinitionResponse = {
  family: 'payments-api',
  deployed_revision: 42,
  task_definition_arn: `arn:aws:ecs:us-east-1:${ACCOUNT_HOMELAB.account_id}:task-definition/payments-api:42`,
  main_container: 'payments-api',
  definition: { family: 'payments-api', cpu: '512', memory: '1024', containerDefinitions: [{ name: 'payments-api', image: 'payments-api:1.18.2', essential: true }] },
  tags: [{ key: 'managed-by', value: 'terraform' }],
  controller: { type: 'ECS', supported: true, name: '' },
  strategy: 'Rolling update · min healthy 100% · max 200% · circuit breaker with rollback is on',
  can: { edit: true, deploy: true, operate: true, admin: false },
  tag: { ...TAG_PROD, inherited: true },
  polled_at: FIXTURE_NOW,
}
export const REVISIONS_PAYMENTS: RevisionsResponse = {
  items: [
    { revision: 43, arn: 'arn:aws:ecs:us-east-1:1:task-definition/payments-api:43', status: 'not_deployed', image_tag: '1.18.3', cpu: '512', memory: '1024', registered_at: FIXTURE_NOW, registered_by: 'pavel' },
    { revision: 42, arn: 'arn:aws:ecs:us-east-1:1:task-definition/payments-api:42', status: 'deployed', image_tag: '1.18.2', cpu: '512', memory: '1024', registered_at: FIXTURE_NOW, registered_by: 'terraform' },
  ],
  deployed_revision: 42,
  rolling_out_revision: null,
}
export const IMAGE_TAGS_PAYMENTS: ImageTagsResponse = {
  source: 'ecr',
  repository: 'payments-api',
  current: '1.18.2',
  tags: [{ tag: '1.18.4', pushed_at: FIXTURE_NOW }],
}
export const DESIRED_COUNT_PAYMENTS: DesiredCountResponse = { desired: 3, running: 3, max: 50, auto_scaling: null }

// ---- Task definition tab (plan 02, task 15) ----
export const TASK_DEFINITION_EDITABLE: TaskDefinitionResponse = {
  ...TASK_DEFINITION_PAYMENTS,
  definition: {
    family: 'payments-api',
    cpu: '512',
    memory: '1024',
    containerDefinitions: [
      { name: 'payments-api', image: 'payments-api:1.18.2', essential: true, environment: [{ name: 'LOG_LEVEL', value: 'info' }] },
    ],
  },
}

// ---- Settings (Task 13) ----
export const SETTINGS: SettingsDTO = { stopped_tasks_window_minutes: 10 }

// ---- Single sign-on (plan 07, Task 12). Shapes follow internal/api/saml_public.go and dto.go. ----

export const AUTH_OPTIONS_OFF: AuthOptionsDTO = { sso: null, password_login: 'all' }
export const AUTH_OPTIONS_ON: AuthOptionsDTO = { sso: { enabled: true, label: 'Sign in with Okta' }, password_login: 'break_glass' }

const SP_CERT: CertDTO = {
  fingerprint: '1B:19:59:31:58:83:07:BF:69:10:3A:11:68:17:BC:01:5F:0E:30:B9:A8:03:04:43:12:B6:F9:16:5F:28:74:86',
  subject: 'CN=harbormaster.example.test',
  not_after: minutesAgo(-365 * 1440),
}
const IDP_CERT: CertDTO = {
  fingerprint: '4B:D3:3B:DB:44:A7:1A:27:60:5B:8A:B5:23:AC:7B:5F:83:20:96:C4:13:F3:AC:E7:3C:5C:2E:22:83:84:DF:87',
  subject: 'CN=idp.example.test',
  not_after: minutesAgo(-200 * 1440),
}
const SP_URLS = {
  sp_entity_id: 'https://harbormaster.example.test/api/auth/saml/metadata',
  acs_url: 'https://harbormaster.example.test/api/auth/saml/acs',
  metadata_path: '/api/auth/saml/metadata',
}

/** Matches the Go defaults for an installation with no configuration row (`buildSSOConfigDTO`). */
export const SSO_UNCONFIGURED: SSOConfigDTO = {
  configured: false,
  enabled: false,
  idp_entity_id: '',
  idp_sso_redirect_url: '',
  idp_certs: [],
  metadata_url: null,
  metadata_fetched_at: null,
  ...SP_URLS,
  sp_cert: null,
  sp_key_state: 'missing',
  name_id_format: 'unspecified',
  attr_email: null,
  attr_display_name: null,
  attr_username: null,
  clock_skew_seconds: 120,
  button_label: 'Sign in with SSO',
  break_glass_admins: 1,
  password_users_blocked_on_enable: 2,
  warnings: [],
}

const CONFIGURED_SSO: SSOConfigDTO = {
  ...SSO_UNCONFIGURED,
  configured: true,
  idp_entity_id: 'https://idp.example.test/saml',
  idp_sso_redirect_url: 'https://idp.example.test/saml/sso',
  idp_certs: [IDP_CERT],
  metadata_url: 'https://idp.example.test/saml/metadata.xml',
  metadata_fetched_at: minutesAgo(60),
  sp_cert: SP_CERT,
  sp_key_state: 'ok',
  name_id_format: 'persistent',
  attr_email: 'email',
  attr_display_name: 'displayName',
  button_label: 'Sign in with Okta',
}

export const SSO_CONFIGURED_OFF: SSOConfigDTO = { ...CONFIGURED_SSO, enabled: false, password_users_blocked_on_enable: 2 }
export const SSO_CONFIGURED_ON: SSOConfigDTO = { ...CONFIGURED_SSO, enabled: true, password_users_blocked_on_enable: 2 }

/** A test sign-in that would create a new user. */
export const SSO_TEST_OK: SSOTestResultDTO = {
  ok: true,
  reason: '',
  message: '',
  name_id: 'carol@example.test',
  name_id_format: 'urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress',
  attributes: { email: ['carol@example.test'], displayName: ['Carol'] },
  username: 'carol',
  outcome: 'would_create',
  existing_user: null,
}

/** A test sign-in whose NameID matches a pending link on `dana`. */
export const SSO_TEST_WOULD_LINK: SSOTestResultDTO = {
  ok: true,
  reason: '',
  message: '',
  name_id: 'dana@example.test',
  name_id_format: 'urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress',
  attributes: { email: ['dana@example.test'], displayName: ['Dana'] },
  username: 'dana',
  outcome: 'would_link',
  existing_user: 'dana',
}

/** A failed test sign-in; the message is the admin-facing text for reason `audience`. */
export const SSO_TEST_FAIL: SSOTestResultDTO = {
  ok: false,
  reason: 'audience',
  message: "The audience does not match the SP entity ID. Check the IdP app's audience setting.",
  name_id: '',
  name_id_format: '',
  attributes: {},
  username: '',
  outcome: '',
  existing_user: null,
}

// ---- analytics (plan: docs/analytics_design/PLAN.md section 1) ----

const ANALYTICS_RANGES: Record<AnalyticsRange, { buckets: number; bucketMinutes: number; bucket: AnalyticsWindow['bucket'] }> = {
  '24h': { buckets: 24, bucketMinutes: 60, bucket: '1h' },
  '7d': { buckets: 42, bucketMinutes: 240, bucket: '4h' },
  '30d': { buckets: 30, bucketMinutes: 1440, bucket: '1d' },
  '90d': { buckets: 45, bucketMinutes: 2880, bucket: '2d' },
}
const ANALYTICS_RANGE_DAYS: Record<AnalyticsRange, number> = { '24h': 1, '7d': 7, '30d': 30, '90d': 90 }

/** The window block for `used`, as the API reports it; `requested` defaults to `used`. */
export function analyticsWindow(
  used: AnalyticsRange,
  options: { requested?: string; clamped?: boolean; rangeApplies?: boolean; retention?: string } = {},
): AnalyticsWindow {
  const { bucket } = ANALYTICS_RANGES[used]
  return {
    requested: options.requested ?? used,
    used,
    clamped: options.clamped ?? false,
    range_applies: options.rangeApplies ?? true,
    bucket,
    from: minutesAgo(ANALYTICS_RANGE_DAYS[used] * 1440),
    to: FIXTURE_NOW,
    retention: options.retention ?? `${ANALYTICS_RANGE_DAYS[used] * 24}h`,
  }
}

/** A series with deterministic values: line i at bucket j is `value(i, j)`; null values mean no sample. */
function analyticsSeries(
  range: AnalyticsRange,
  lines: { key: string; label: string; expected?: boolean }[],
  value: (line: number, bucket: number) => number | null,
): AnalyticsSeries {
  const { buckets, bucketMinutes } = ANALYTICS_RANGES[range]
  return {
    timestamps: Array.from({ length: buckets }, (_, j) => minutesAgo((buckets - 1 - j) * bucketMinutes)),
    series: lines.map((line, i) => ({
      key: line.key,
      label: line.label,
      expected: line.expected ?? false,
      values: Array.from({ length: buckets }, (_, j) => value(i, j)),
    })),
  }
}

const analyticsWave = (line: number, bucket: number, base: number, amplitude: number) => base + ((bucket * 7 + line * 3) % 5) * amplitude

const source = (table: string, reads: string, kept: string, setting: string | null, windowUsed: string | null = null): AnalyticsSource => ({
  table,
  reads,
  kept,
  setting,
  window_used: windowUsed,
})

const analyticsPaymentsId = serviceId('482109375512', 'us-east-1', 'core-services', 'payments-api')
const analyticsCatalogId = serviceId('482109375512', 'us-east-1', 'core-services', 'catalog')
const analyticsCoreClusterId = clusterId('482109375512', 'us-east-1', 'core-services')
const analyticsStagingClusterId = clusterId('731022849906', 'us-east-1', 'staging-web')

export const ANALYTICS_DELIVERY: AnalyticsDelivery = {
  tab: 'delivery',
  window: analyticsWindow('30d', { retention: '2160h' }),
  sources: [source('deployment_history', 'Rollouts with outcome, revisions and timings', '90 days', 'HM_RETENTION_DEPLOYMENT_HISTORY')],
  polled_at: FIXTURE_NOW,
  kpis: {
    rollouts: 48,
    rollouts_per_day: 1.6,
    change_failure_rate: 0.083,
    median_rollout_s: 312,
    p90_rollout_s: 780,
    time_to_recover_s: 1080,
    rollbacks: 2,
    superseded: 3,
  },
  rollouts: analyticsSeries(
    '30d',
    [
      { key: 'completed', label: 'Completed' },
      { key: 'failed', label: 'Failed' },
      { key: 'superseded', label: 'Superseded' },
    ],
    (line, j) => (line === 0 ? 1 + (j % 3) : line === 1 ? (j % 7 === 0 ? 1 : 0) : j % 11 === 0 ? 1 : 0),
  ),
  failure_rate: analyticsSeries('30d', [{ key: 'failure_rate', label: 'Change failure rate' }], (_, j) => (j % 6 === 5 ? null : analyticsWave(0, j, 2, 2))),
  duration: analyticsSeries(
    '30d',
    [
      { key: 'median', label: 'Median' },
      { key: 'p90', label: 'p90' },
    ],
    (line, j) => (line === 0 ? 280 + (j % 4) * 20 : 700 + (j % 5) * 30),
  ),
  services: {
    rows: [
      {
        service_id: analyticsPaymentsId,
        service: 'payments-api',
        cluster: 'core-services',
        account_id: '482109375512',
        tag: TAG_PROD,
        rollouts: 12,
        failed: 2,
        failure_rate: 0.167,
        median_s: 420,
        recover_s: 1500,
        last_failed_at: minutesAgo(3 * 1440),
      },
      {
        service_id: analyticsCatalogId,
        service: 'catalog',
        cluster: 'core-services',
        account_id: '482109375512',
        tag: { ...TAG_PROD, inherited: true },
        rollouts: 9,
        failed: 0,
        failure_rate: 0,
        median_s: 240,
        recover_s: null,
        last_failed_at: null,
      },
    ],
    total: 2,
  },
  failure_reasons: {
    available: false,
    data_since: null,
    needs: { table: 'deployment_history.failure_reason', size: '< 1 MB', how: 'The outcome logger records the reason on each failed rollout.' },
    data: null,
  },
  recent_failures: {
    available: false,
    data_since: null,
    needs: { table: 'deployment_history.failure_reason', size: '< 1 MB', how: 'The outcome logger records the reason on each failed rollout.' },
    data: null,
  },
}

export const ANALYTICS_RELIABILITY: AnalyticsReliability = {
  tab: 'reliability',
  window: analyticsWindow('7d', { requested: '30d', clamped: true, retention: '168h' }),
  sources: [
    source('service_counts_history', '5-minute running and desired counts', '7 days', 'HM_RETENTION_COUNTS_HISTORY'),
    source('tasks', 'Stopped tasks with stop reason and exit codes', '7 days', 'HM_RETENTION_STOPPED_TASKS'),
    source('service_events', 'Events mentioning "unable to place"', '30 days', 'HM_RETENTION_SERVICE_EVENTS', '30d'),
  ],
  polled_at: FIXTURE_NOW,
  kpis: {
    availability: 0.9962,
    minutes_below_desired: 95,
    services_below: 2,
    task_stops: 31,
    unexpected_stops: 6,
    out_of_memory: 1,
    placement_failures: 2,
  },
  availability: analyticsSeries('7d', [{ key: 'availability', label: 'Availability' }], (_, j) => 99 + ((j * 3) % 10) / 10),
  services_below: analyticsSeries('7d', [{ key: 'services_below', label: 'Services below desired' }], (_, j) => (j % 9 === 0 ? 2 : j % 4 === 0 ? 1 : 0)),
  stops_by_reason: analyticsSeries(
    '7d',
    [
      { key: 'deployment', label: 'Deployment', expected: true },
      { key: 'scale_in', label: 'Scale-in', expected: true },
      { key: 'essential_exit', label: 'Essential container exited' },
      { key: 'oom', label: 'Out of memory' },
    ],
    (line, j) => (line < 2 ? (j + line) % 4 : j % (6 + line) === 0 ? 1 : 0),
  ),
  unexpected_by_zone: analyticsSeries(
    '7d',
    [
      { key: 'us-east-1a', label: 'us-east-1a' },
      { key: 'us-east-1b', label: 'us-east-1b' },
    ],
    (line, j) => (j % (5 + line) === 0 ? 1 : 0),
  ),
  exit_codes: analyticsSeries(
    '7d',
    [
      { key: '1', label: '1' },
      { key: '137', label: '137' },
      { key: 'other', label: 'other' },
    ],
    (line, j) => (j % (4 + line * 3) === 0 ? 1 : 0),
  ),
  least_available: {
    rows: [
      {
        service_id: analyticsPaymentsId,
        service: 'payments-api',
        cluster: 'core-services',
        tag: TAG_PROD,
        availability: 0.9921,
        minutes_below: 80,
        unexpected_stops: 4,
        top_reason: 'Essential container exited',
      },
      { service_id: analyticsCatalogId, service: 'catalog', cluster: 'core-services', tag: { ...TAG_PROD, inherited: true }, availability: 0.9994, minutes_below: 15, unexpected_stops: 0, top_reason: null },
    ],
    total: 2,
  },
  availability_90d: {
    available: false,
    data_since: null,
    needs: { table: 'service_availability_daily', size: '~35 MB a year', how: 'A nightly rollup of counts history and stopped tasks.' },
    data: null,
  },
  below_desired_90d: {
    available: true,
    data_since: minutesAgo(20 * 1440),
    needs: null,
    data: analyticsSeries('90d', [{ key: 'minutes_below', label: 'Minutes below desired' }], (_, j) => (j < 35 ? null : (j * 5) % 40)),
  },
  getting_worse: {
    available: false,
    data_since: null,
    needs: { table: 'service_availability_daily', size: '~35 MB a year', how: 'A nightly rollup of counts history and stopped tasks.' },
    data: null,
  },
}

export const ANALYTICS_CAPACITY: AnalyticsCapacity = {
  tab: 'capacity',
  window: analyticsWindow('7d', { requested: '30d', rangeApplies: false, retention: '168h' }),
  sources: [
    source('tasks', 'Running tasks: cpu, memory, capacity provider', 'latest poll', null),
    source('service_counts_history', 'Running count over time, for the 7-day trend', '7 days', 'HM_RETENTION_COUNTS_HISTORY'),
    source('subnets', 'Available IP addresses per subnet', 'latest poll', null),
  ],
  polled_at: FIXTURE_NOW,
  kpis: { reserved_vcpu: 14.5, reserved_mem_gib: 36, running_tasks: 27, services: 15, spot_share: 0.22, at_scaling_max: 1, subnets_low_ips: 1 },
  reserved_vcpu_7d: analyticsSeries(
    '7d',
    [
      { key: String(TAG_PROD.id), label: TAG_PROD.name },
      { key: String(TAG_STAGING.id), label: TAG_STAGING.name },
      { key: String(TAG_DEV.id), label: TAG_DEV.name },
    ],
    (line, j) => 2 + line * 2 + (j % 3) * 0.5,
  ),
  vcpu_by_account: {
    total: 14.5,
    slices: [
      { key: '1', label: ACCOUNT_HOMELAB.name, value: 12 },
      { key: '2', label: ACCOUNT_STAGING.name, value: 2.5 },
    ],
  },
  tasks_by_capacity_provider: {
    total: 27,
    slices: [
      { key: 'FARGATE', label: 'FARGATE', value: 21 },
      { key: 'FARGATE_SPOT', label: 'FARGATE_SPOT', value: 6 },
    ],
  },
  clusters: {
    rows: [
      { cluster_id: analyticsCoreClusterId, cluster: 'core-services', account_id: '482109375512', account_name: ACCOUNT_HOMELAB.name, tag: TAG_PROD, tasks: 12, vcpu: 8, mem_gib: 18, share: 0.55, spot_share: 0.1 },
      { cluster_id: analyticsStagingClusterId, cluster: 'staging-web', account_id: '731022849906', account_name: ACCOUNT_STAGING.name, tag: TAG_STAGING, tasks: 4, vcpu: 2.5, mem_gib: 5, share: 0.17, spot_share: 0.5 },
    ],
    total: 2,
  },
  at_scaling_max: [
    { service_id: analyticsPaymentsId, service: 'payments-api', cluster: 'core-services', desired: 6, max_capacity: 6, at_max_since: minutesAgo(180) },
    { service_id: analyticsCatalogId, service: 'catalog', cluster: 'core-services', desired: 4, max_capacity: 4, at_max_since: null },
  ],
  subnets_low: [
    { subnet_id: 'subnet-0a1b2c3d', name: 'private-a', az: 'us-east-1a', account_id: '482109375512', region: 'us-east-1', available_ips: 20, total_ips: 256, used_pct: 0.92 },
  ],
  reserved_vs_used_90d: {
    available: false,
    data_since: null,
    needs: { table: 'cluster_capacity_daily', size: '~5 MB a year', how: 'Running tasks sampled on the counts-history poll.' },
    data: null,
  },
  spot_share_90d: {
    available: false,
    data_since: null,
    needs: { table: 'cluster_capacity_daily', size: '~5 MB a year', how: 'Running tasks sampled on the counts-history poll.' },
    data: null,
  },
  memory_90d: {
    available: false,
    data_since: null,
    needs: { table: 'service_util_daily', size: '~8 MB for 90 days', how: 'One CloudWatch batch per account and region a day.' },
    data: null,
  },
  over_reserved: {
    available: false,
    data_since: null,
    needs: { table: 'service_util_daily', size: '~8 MB for 90 days', how: 'One CloudWatch batch per account and region a day.' },
    data: null,
  },
}

export const ANALYTICS_OPERATIONS: AnalyticsOperations = {
  tab: 'operations',
  window: analyticsWindow('30d', { retention: '2160h' }),
  sources: [
    source('operation_log', 'Operations by Harbormaster users, classified by kind', '90 days', 'HM_RETENTION_OPERATION_LOG'),
    source('revision_records', 'Task definition revisions registered', 'while the service exists', null),
  ],
  polled_at: FIXTURE_NOW,
  kpis: { operations: 120, people: 4, revisions_registered: 18, scale_changes: 22, restarts: 9, tasks_stopped: 5, terminal_sessions: 7 },
  by_type: analyticsSeries(
    '30d',
    [
      { key: 'deploy', label: 'Deploy' },
      { key: 'rollback', label: 'Rollback' },
      { key: 'task_definition', label: 'Task definition' },
      { key: 'scale', label: 'Scale' },
      { key: 'restart', label: 'Restart' },
      { key: 'stop_task', label: 'Stop task' },
      { key: 'terminal', label: 'Terminal' },
    ],
    (line, j) => (j + line) % (line + 2) === 0 ? 1 : 0,
  ),
  hands_on: analyticsSeries(
    '30d',
    [
      { key: 'restart', label: 'Restart' },
      { key: 'stop_task', label: 'Stop task' },
      { key: 'terminal', label: 'Terminal' },
    ],
    (line, j) => (j % (3 + line) === 0 ? 1 : 0),
  ),
  restarts_per_100_deploys: analyticsSeries('30d', [{ key: 'restarts_per_100_deploys', label: 'Restarts per 100 deploys' }], (_, j) => (j % 5 === 4 ? null : 10 + (j % 4) * 5)),
  who: {
    rows: [
      { username: 'pavel', total: 70, deploy: 40, restart: 5, scale: 15, terminal: 4, last_at: minutesAgo(25) },
      { username: 'omer', total: 30, deploy: 10, restart: 4, scale: 7, terminal: 3, last_at: minutesAgo(600) },
    ],
    total: 2,
  },
  most_restarted: {
    rows: [{ service_id: analyticsPaymentsId, service: 'payments-api', cluster: 'core-services', tag: TAG_PROD, restarts: 5, people: 2, last_at: minutesAgo(120) }],
    total: 1,
  },
}

// ---- analytics with the Phase 2 rollups available ------------------------------------------------

/** A 90-day daily series ending today, as the rollup panels return it: `value` is null before the rollup began. */
function analyticsDailySeries(lines: { key: string; label: string }[], value: (line: number, daysAgo: number) => number | null): AnalyticsSeries {
  return {
    timestamps: Array.from({ length: 90 }, (_, j) => minutesAgo((89 - j) * 1440)),
    series: lines.map((line, i) => ({
      key: line.key,
      label: line.label,
      expected: false,
      values: Array.from({ length: 90 }, (_, j) => value(i, 89 - j)),
    })),
  }
}

const availablePanel = <T>(dataSinceDaysAgo: number, data: T): Proposed<T> => ({
  available: true,
  data_since: minutesAgo(dataSinceDaysAgo * 1440),
  needs: null,
  data,
})

/** Reliability once service_availability_daily has 45 days of rows: partial 90-day charts and a getting-worse table. */
export const ANALYTICS_RELIABILITY_DAILY: AnalyticsReliability = {
  ...ANALYTICS_RELIABILITY,
  sources: [
    ...ANALYTICS_RELIABILITY.sources,
    source('service_availability_daily', 'Daily availability and time below desired per service', '8760h', 'HM_RETENTION_AVAILABILITY_DAILY'),
  ],
  availability_90d: availablePanel(
    45,
    analyticsDailySeries([{ key: 'availability', label: 'Availability' }], (_, daysAgo) => (daysAgo > 45 ? null : 99.2 + ((daysAgo * 7) % 8) / 10)),
  ),
  below_desired_90d: availablePanel(
    45,
    analyticsDailySeries([{ key: 'minutes_below', label: 'Minutes below desired' }], (_, daysAgo) => (daysAgo > 45 ? null : (daysAgo * 13) % 90)),
  ),
  getting_worse: availablePanel(45, {
    rows: [
      { service_id: analyticsPaymentsId, service: 'payments-api', cluster: 'core-services', tag: TAG_PROD, previous: 0.9991, current: 0.9874, change_pts: -1.17 },
      { service_id: analyticsCatalogId, service: 'catalog', cluster: 'core-services', tag: { ...TAG_PROD, inherited: true }, previous: 0.9999, current: 0.9981, change_pts: -0.18 },
    ],
    total: 2,
  }),
}

/** Capacity once cluster_capacity_daily and service_util_daily have 30 days of rows. */
export const ANALYTICS_CAPACITY_DAILY: AnalyticsCapacity = {
  ...ANALYTICS_CAPACITY,
  sources: [
    ...ANALYTICS_CAPACITY.sources,
    source('cluster_capacity_daily', 'Daily average reserved vCPU and memory per cluster', '8760h', 'HM_RETENTION_CAPACITY_DAILY'),
    source('service_util_daily', 'Daily CPU and memory use as a share of reserved', '2160h', 'HM_RETENTION_UTIL_DAILY'),
  ],
  reserved_vs_used_90d: availablePanel(
    30,
    analyticsDailySeries(
      [
        { key: 'reserved', label: 'Reserved' },
        { key: 'used_p95', label: 'Used p95' },
        { key: 'used_avg', label: 'Used avg' },
      ],
      (line, daysAgo) => (daysAgo > 30 ? null : [14, 8.5, 5][line] + (daysAgo % 4) / 4),
    ),
  ),
  spot_share_90d: availablePanel(30, analyticsDailySeries([{ key: 'spot_share', label: 'Spot share' }], (_, daysAgo) => (daysAgo > 30 ? null : 22 + (daysAgo % 5)))),
  memory_90d: availablePanel(
    30,
    analyticsDailySeries(
      [
        { key: 'reserved', label: 'Reserved' },
        { key: 'used_p95', label: 'Used p95' },
      ],
      (line, daysAgo) => (daysAgo > 30 ? null : [28, 17][line] + (daysAgo % 3) / 2),
    ),
  ),
  over_reserved: availablePanel(30, {
    rows: [
      {
        service_id: analyticsPaymentsId,
        service: 'payments-api',
        cluster: 'core-services',
        tag: TAG_PROD,
        reserved_vcpu: 2,
        used_p95_vcpu: 0.5,
        reserved_mem_gib: 4,
        used_p95_mem_gib: 1.4,
        wasted_vcpu: 9,
      },
    ],
    total: 1,
  }),
}

/** Delivery once rollouts record why they failed (first reason 12 days ago). */
export const ANALYTICS_DELIVERY_FAILURES: AnalyticsDelivery = {
  ...ANALYTICS_DELIVERY,
  failure_reasons: availablePanel(
    12,
    analyticsSeries(
      '30d',
      [
        { key: 'circuit_breaker', label: 'Circuit breaker' },
        { key: 'health_check', label: 'Health check' },
        { key: 'failed_to_start', label: 'Failed to start' },
        { key: 'out_of_memory', label: 'Out of memory' },
      ],
      (line, bucket) => (bucket < 18 ? 0 : (bucket + line) % 4 === 0 ? 1 : 0),
    ),
  ),
  recent_failures: availablePanel(12, {
    rows: [
      {
        at: minutesAgo(180),
        service_id: analyticsPaymentsId,
        service: 'payments-api',
        cluster: 'core-services',
        from_revision: 41,
        to_revision: 42,
        reason: 'circuit_breaker',
        detail: 'ECS deployment circuit breaker: tasks failed to start.',
        ran_for_s: 412,
      },
      {
        at: minutesAgo(3000),
        service_id: analyticsCatalogId,
        service: 'catalog',
        cluster: 'core-services',
        from_revision: null,
        to_revision: 7,
        reason: 'out_of_memory',
        detail: 'OutOfMemoryError: Container killed due to memory usage',
        ran_for_s: null,
      },
    ],
    total: 2,
  }),
}
