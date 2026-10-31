// Deterministic API fixtures shaped like the fake estate (two accounts, six clusters, fifteen services).
// Every body is derived from one set of service definitions so counts, ids and statuses agree across screens.
import type {
  AccountsDTO,
  AuditDTO,
  ClusterCardDTO,
  ClusterDTO,
  ClusterServiceDTO,
  ClusterStatus,
  DeploysDayDTO,
  EventDTO,
  ListDTO,
  MeDTO,
  OverviewDTO,
  PermissionsDTO,
  PointDTO,
  PollerDTO,
  ReasonCountDTO,
  ServiceDTO,
  SlotDTO,
  Status,
  StopsHourDTO,
  TagAssignmentsDTO,
  TagColor,
  TagDTO,
  TagsDTO,
  TaskDTO,
  TasksListDTO,
  UsersDTO,
} from '../api/types'

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
): Pick<TaskDTO, 'id' | 'short_id' | 'arn' | 'cluster' | 'az' | 'subnet_id' | 'private_ip'> {
  const arn = `arn:aws:ecs:${cluster.region}:${cluster.account.account_id}:task/${cluster.name}/${hex(seed, 32)}`
  const [az, subnet] = AZ_SUBNETS[cluster.region][index % 2]
  const octet = REGIONS.indexOf(cluster.region) + 1
  return {
    id: taskId(cluster.account.account_id, cluster.region, arn),
    short_id: hex(seed, 8),
    arn,
    cluster: clusterRefOf(cluster),
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

const isProblem = (task: TaskDTO) =>
  (task.status === 'RUNNING' && task.health === 'UNHEALTHY') || (task.status === 'STOPPED' && task.exit_code !== 0)

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
  const running = items.filter((t) => t.status === 'RUNNING')
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
    counts: { problems: items.filter(isProblem).length, running: running.length, stopped: stopped.length, all: items.length },
    summary: {
      by_status: {
        running_healthy: running.filter((t) => t.health !== 'UNHEALTHY').length,
        running_unhealthy: running.filter((t) => t.health === 'UNHEALTHY').length,
        stopped_error: stopped.filter((t) => t.exit_code !== 0).length,
        stopped_normal: stopped.filter((t) => t.exit_code === 0).length,
      },
      by_az: [...azCounts].map(([az, count]) => ({ az, count })).sort((a, b) => a.az.localeCompare(b.az)),
      by_cluster: [...clusters.values()],
      stop_reasons: stopReasons(items),
    },
  }
}

/** The list the API returns for `/api/tasks?filter=&q=`: items are filtered, counts are for the filtered scope. */
export function tasksResponse(filter: string | null, q: string | null): TasksListDTO {
  const needle = (q ?? '').toLowerCase()
  const matchesQuery = (t: TaskDTO) =>
    !needle || [t.short_id, t.family, t.service?.name ?? '', t.cluster.name, t.private_ip].some((v) => v.toLowerCase().includes(needle))
  const scoped = ALL_TASKS.filter(matchesQuery)
  const body = tasksList(scoped)
  const items =
    filter === 'problems' ? scoped.filter(isProblem)
    : filter === 'running' ? scoped.filter((t) => t.status === 'RUNNING')
    : filter === 'stopped' ? scoped.filter((t) => t.status === 'STOPPED')
    : scoped
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
  const ecs = (minutes: number, text: string): EventDTO => ({ ts: minutesAgo(minutes), source: 'ecs', actor: '', ...base, text })
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
  per_cluster: CLUSTERS.items.map((c) => ({ id: c.id, name: c.name, running: c.running, desired: c.desired })),
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
}

const plainTag = (t: TagDTO): TagDTO => ({ ...t, inherited: false })

export const USERS: UsersDTO = {
  items: [
    { id: 1, username: 'pavel', admin: true, must_change_password: false, last_sign_in_at: minutesAgo(2), can_see: [TAG_DEV, TAG_STAGING, TAG_PROD].map(plainTag) },
    { id: 2, username: 'dana', admin: false, must_change_password: false, last_sign_in_at: minutesAgo(50), can_see: [TAG_DEV, TAG_STAGING, TAG_PROD].map(plainTag) },
    { id: 3, username: 'omer', admin: false, must_change_password: false, last_sign_in_at: minutesAgo(26 * 60), can_see: [TAG_DEV, TAG_STAGING].map(plainTag) },
    { id: 4, username: 'oncall', admin: false, must_change_password: false, last_sign_in_at: minutesAgo(3 * 1440), can_see: [TAG_STAGING, TAG_PROD].map(plainTag) },
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
