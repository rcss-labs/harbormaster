import type { ReactNode } from 'react'
import { Link } from 'react-router'
import type { TaskDTO } from '../api/types'
import { Ago } from './Ago'
import { DataTable, type DataTableColumn, type RowStatus } from './DataTable'
import type { PillKind } from './Pill'
import { RowMenu, RowMenuButton, RowMenuLink } from './RowMenu'
import { StatusDot } from './StatusDot'
import { StopCauseChip, stoppedStatus } from './stopStatus'
import { useNarrow } from './useNarrow'
import styles from './TaskTable.module.css'

/** Utilisation in percent of the reserved size, keyed by the task's short id (the id CloudWatch reports). */
export type TaskUtilisation = Record<string, { cpu: number | null; mem: number | null }>

interface TaskTableProps {
  items: TaskDTO[]
  /** Adds the Service column (the Tasks page); the service Tasks tab leaves it out. */
  withService: boolean
  /** Cursor for the next page; "Load more" shows only when set. */
  next?: string
  onMore(): void
  utilisation?: TaskUtilisation
  /** Opens the task's details drawer; the task id is the control. */
  onOpen(task: TaskDTO): void
  /** The Stop row action; the host owns the confirm dialog. */
  onStop(task: TaskDTO): void
  /** Keys of the columns to show (see TASK_COLUMNS); all when omitted. */
  visibleColumns?: string[]
}

export interface TaskColumnOption {
  key: string
  label: string
  locked?: boolean
}

/** Column keys and picker labels shared by the Tasks page and the service Tasks tab. Task, Service and Status always show. */
export const TASK_COLUMNS: TaskColumnOption[] = [
  { key: 'task', label: 'Task', locked: true },
  { key: 'service', label: 'Service', locked: true },
  { key: 'status', label: 'Status', locked: true },
  { key: 'health', label: 'Health' },
  { key: 'revision', label: 'Rev' },
  { key: 'ip', label: 'IP · Subnet' },
  { key: 'capacity', label: 'Capacity' },
  { key: 'cpu', label: 'CPU' },
  { key: 'memory', label: 'Memory' },
  { key: 'started', label: 'Started' },
  { key: 'reason', label: 'Stopped reason' },
]

/** Sized to the widest row's buttons, which the table's shared grid tracks make the same for the header. */
export const ACTIONS_WIDTH = 'max-content'

const PROGRESS_STATUSES = ['PROVISIONING', 'PENDING', 'ACTIVATING']
const titleCase = (status: string) => status.charAt(0) + status.slice(1).toLowerCase()

/** vCPU from ECS CPU units: up to two decimals, trailing zeros dropped (256 → 0.25, 1024 → 1). */
export const formatVcpu = (cpuUnits: string) => String(+(Number(cpuUnits) / 1024).toFixed(2))

export function taskStatus(task: TaskDTO): { kind: PillKind; label: string } {
  if (task.status === 'RUNNING') return { kind: 'ok', label: 'Running' }
  if (PROGRESS_STATUSES.includes(task.status)) return { kind: 'info', label: titleCase(task.status) }
  if (task.status === 'STOPPED') return stoppedStatus(task)
  // DEACTIVATING, STOPPING, DEPROVISIONING and anything new.
  return { kind: 'mute', label: titleCase(task.status) }
}

/** Row stripe: a failed stop is red, an expected stop teal; a running task failing its health check is amber. */
export function taskStripe(task: TaskDTO): RowStatus | undefined {
  if (task.status === 'STOPPED' && task.stop_kind === 'error') return 'bad'
  if (task.status === 'STOPPED' && task.stop_kind === 'expected') return 'expected'
  if (task.status === 'RUNNING' && task.health === 'UNHEALTHY') return 'warn'
  return undefined
}

/** Service tasks open the service's Terminal tab; standalone tasks have no service and get their own page. */
export const terminalPath = (task: TaskDTO) =>
  task.service
    ? `/services/${task.service.id}/terminal?task=${encodeURIComponent(task.id)}`
    : `/tasks/${encodeURIComponent(task.short_id)}/terminal`

/** "34% of 512 MiB" when usage is known, else the reserved size alone. */
const usageOrReserved = (usedPercent: number | null | undefined, reserved: string) =>
  usedPercent === null || usedPercent === undefined ? reserved : `${Math.round(usedPercent)}% of ${reserved}`

export function TaskStatusCell({ task }: { task: TaskDTO }) {
  const status = taskStatus(task)
  return (
    <span className={styles.statusCell}>
      <StatusDot kind={status.kind}>{status.label}</StatusDot>
      {task.status === 'STOPPED' && <StopCauseChip cause={task.stop_cause} />}
    </span>
  )
}

export function HealthCell({ task }: { task: TaskDTO }) {
  if (task.status !== 'RUNNING') return <span className={styles.muted}>–</span>
  if (task.health === 'HEALTHY') return <span className={styles.healthy}>Healthy</span>
  if (task.health === 'UNHEALTHY') return <span className={styles.unhealthy}>Unhealthy</span>
  return <span className={styles.muted}>No check</span>
}

export function ServiceCell({ task }: { task: TaskDTO }) {
  if (task.service) {
    return (
      <span className={styles.service}>
        <Link to={`/services/${task.service.id}/tasks`} className={styles.accent} title={task.service.name}>
          {task.service.name}
        </Link>
        <span className={styles.sub} title={task.cluster.name}>
          {task.cluster.name}
        </span>
      </span>
    )
  }
  const owner = `Standalone · started by ${task.started_by}`
  return (
    <span className={styles.service}>
      <span title={task.group}>{task.group}</span>
      <span className={styles.sub} title={owner}>
        {owner}
      </span>
    </span>
  )
}

/** The task id as a button that opens the details drawer. */
export function TaskIdButton({ task, nested = false, onOpen }: { task: TaskDTO; nested?: boolean; onOpen(task: TaskDTO): void }) {
  const shortId = task.short_id.slice(0, 12)
  return (
    <button
      type="button"
      className={`${styles.idButton} ${nested ? styles.nested : ''}`.trim()}
      title={task.id}
      aria-label={`Open details for task ${shortId}`}
      onClick={() => onOpen(task)}
    >
      {shortId}
    </button>
  )
}

export function IpCell({ task }: { task: TaskDTO }) {
  const where = `${task.subnet_id} · ${task.az}`
  return (
    <span className={styles.stack}>
      <span className={styles.mono}>{task.private_ip}</span>
      <span className={styles.subMono} title={where}>
        {where}
      </span>
    </span>
  )
}

export function ReasonCell({ task }: { task: TaskDTO }) {
  const { stopped_reason: reason, stopped_at: stoppedAt } = task
  return (
    <span className={styles.reason} title={reason}>
      {reason}
      {stoppedAt && (
        <span className={styles.muted}>
          {reason ? ' · ' : ''}
          <Ago ts={stoppedAt} />
        </span>
      )}
    </span>
  )
}

export const cpuText = (task: TaskDTO, usage?: { cpu: number | null }) => usageOrReserved(usage?.cpu, `${formatVcpu(task.cpu)} vCPU`)
export const memoryText = (task: TaskDTO, usage?: { mem: number | null }) => usageOrReserved(usage?.mem, `${task.memory} MiB`)

/** Row actions: Logs (when asked for and the task has a service), Terminal and Stop under the existing permission rules. */
export function TaskActions({ task, withLogs = false, onStop }: { task: TaskDTO; withLogs?: boolean; onStop(task: TaskDTO): void }) {
  const narrow = useNarrow()
  const isRunning = task.status === 'RUNNING'
  if (narrow) {
    const showTerminal = isRunning && task.can.terminal && task.exec_enabled
    const showStop = isRunning && task.can.operate
    const showLogs = withLogs && task.service
    if (!showLogs && !showTerminal && !showStop) return null
    return (
      <RowMenu label={`Actions for task ${task.short_id.slice(0, 12)}`}>
        {showLogs && task.service && <RowMenuLink to={`/services/${task.service.id}/logs`}>Logs</RowMenuLink>}
        {showTerminal && <RowMenuLink to={terminalPath(task)}>Terminal</RowMenuLink>}
        {showStop && (
          <RowMenuButton danger onClick={() => onStop(task)}>
            Stop
          </RowMenuButton>
        )}
      </RowMenu>
    )
  }
  return (
    <span className={styles.actions}>
      {withLogs && task.service && (
        <Link to={`/services/${task.service.id}/logs`} className={styles.btn}>
          Logs
        </Link>
      )}
      {isRunning && task.can.terminal && task.exec_enabled && (
        <Link to={terminalPath(task)} className={styles.btn}>
          Terminal
        </Link>
      )}
      {isRunning && task.can.operate && (
        <button type="button" className={`${styles.btn} ${styles.danger}`} onClick={() => onStop(task)}>
          Stop
        </button>
      )}
    </span>
  )
}

/** Columns in table order, keyed by TASK_COLUMNS; the Tasks page wraps these to add its repeat-group rows. */
export function taskColumns({
  withService,
  utilisation,
  onOpen,
  onStop,
  withLogs = false,
}: {
  withService: boolean
  utilisation?: TaskUtilisation
  onOpen(task: TaskDTO): void
  onStop(task: TaskDTO): void
  withLogs?: boolean
}): DataTableColumn<TaskDTO>[] {
  const columns: DataTableColumn<TaskDTO>[] = [
    { key: 'task', header: 'Task', width: '120px', render: (task) => <TaskIdButton task={task} onOpen={onOpen} /> },
  ]
  if (withService) columns.push({ key: 'service', header: 'Service', width: 'minmax(0,1.1fr)', render: (task) => <ServiceCell task={task} /> })
  columns.push(
    { key: 'status', header: 'Status', width: '230px', render: (task) => <TaskStatusCell task={task} /> },
    { key: 'health', header: 'Health', width: '84px', render: (task) => <HealthCell task={task} /> },
    { key: 'revision', header: 'Revision', width: '76px', render: (task) => <span className={styles.mono}>:{task.revision}</span> },
    { key: 'ip', header: 'Subnet / IP', width: 'minmax(150px,1fr)', render: (task) => <IpCell task={task} /> },
    { key: 'capacity', header: 'Capacity', width: '116px', render: (task) => <span className={styles.small}>{task.capacity}</span> },
    {
      key: 'cpu',
      header: 'CPU',
      width: '150px',
      align: 'right',
      render: (task) => <span className={styles.mono}>{cpuText(task, utilisation?.[task.short_id])}</span>,
    },
    {
      key: 'memory',
      header: 'Memory',
      width: '150px',
      align: 'right',
      render: (task) => <span className={styles.mono}>{memoryText(task, utilisation?.[task.short_id])}</span>,
    },
    {
      key: 'started',
      header: 'Started',
      width: '84px',
      render: (task) => (task.started_at ? <Ago ts={task.started_at} /> : <span className={styles.muted}>–</span>),
    },
    { key: 'reason', header: 'Stopped reason', width: 'minmax(0,2fr)', render: (task) => <ReasonCell task={task} /> },
    {
      key: 'actions',
      header: '',
      phone: 'action',
      width: ACTIONS_WIDTH,
      align: 'right',
      render: (task) => <TaskActions task={task} withLogs={withLogs} onStop={onStop} />,
    },
  )
  return columns
}

/** Task rows for the service Tasks tab (and any flat task list): sticky actions, column visibility, "Load more". */
export function TaskTable({ items, withService, next, onMore, utilisation, onOpen, onStop, visibleColumns }: TaskTableProps): ReactNode {
  const columns = taskColumns({ withService, utilisation, onOpen, onStop })
  return (
    <div className={styles.panel}>
      <DataTable
        label="Tasks"
        columns={columns}
        rows={items}
        rowKey={(task) => task.id}
        rowStatus={taskStripe}
        visibleColumns={visibleColumns ? [...visibleColumns, 'actions'] : undefined}
        stickyActions
        minWidth={withService ? 1500 : 1350}
        empty="No tasks match."
      />
      {next && (
        <div className={styles.more}>
          <button type="button" className={styles.btn} onClick={onMore}>
            Load more
          </button>
        </div>
      )}
    </div>
  )
}
