import { useState } from 'react'
import { Link } from 'react-router'
import type { TaskDTO } from '../../api/types'
import { Ago } from '../../components/Ago'
import { DataTable, type DataTableColumn, type RowStatus } from '../../components/DataTable'
import type { PillKind } from '../../components/Pill'
import { StatusDot } from '../../components/StatusDot'
import { StopCauseChip, stoppedStatus } from '../../components/stopStatus'
import { StopTaskDialog } from '../../components/StopTaskDialog'
import { isProblem, type TaskTableRow } from './taskRows'
import styles from './TasksTable.module.css'

export const PAGE_SIZE = 50

const PROGRESS_STATUSES = ['PROVISIONING', 'PENDING', 'ACTIVATING']
const titleCase = (status: string) => status.charAt(0) + status.slice(1).toLowerCase()

/** vCPU from ECS CPU units: up to two decimals, trailing zeros dropped (256 → 0.25, 1024 → 1). */
const formatVcpu = (cpuUnits: string) => String(+(Number(cpuUnits) / 1024).toFixed(2))

function statusOf(task: TaskDTO): { kind: PillKind; label: string } {
  if (task.status === 'RUNNING') return { kind: 'ok', label: 'Running' }
  if (PROGRESS_STATUSES.includes(task.status)) return { kind: 'info', label: titleCase(task.status) }
  if (task.status === 'STOPPED') return stoppedStatus(task)
  return { kind: 'mute', label: titleCase(task.status) }
}

/** Row stripe: a failed stop is red, an expected stop teal; a running task failing its health check is amber. */
function stripeOf(task: TaskDTO): RowStatus | undefined {
  if (task.status === 'STOPPED' && task.stop_kind === 'expected') return 'expected'
  if (!isProblem(task)) return undefined
  return task.status === 'STOPPED' ? 'bad' : 'warn'
}

/** Service tasks open the service's Terminal tab; standalone tasks have no service and get their own page. */
const terminalPath = (task: TaskDTO) =>
  task.service
    ? `/services/${task.service.id}/terminal?task=${encodeURIComponent(task.id)}`
    : `/tasks/${encodeURIComponent(task.short_id)}/terminal`

function HealthCell({ task }: { task: TaskDTO }) {
  if (task.status !== 'RUNNING') return <span className={styles.dim}>–</span>
  if (task.health === 'HEALTHY') return <span className={styles.healthy}>Healthy</span>
  if (task.health === 'UNHEALTHY') return <span className={styles.unhealthy}>Unhealthy</span>
  return <span className={styles.dim}>No check</span>
}

function ServiceCell({ task }: { task: TaskDTO }) {
  if (task.service) {
    return (
      <span className={styles.service}>
        <Link to={`/services/${task.service.id}/tasks`} className={styles.link}>
          {task.service.name}
        </Link>
        <span className={styles.sub}>{task.cluster.name}</span>
      </span>
    )
  }
  return (
    <span className={styles.service}>
      <span>{task.group}</span>
      <span className={styles.sub}>Standalone · started by {task.started_by}</span>
    </span>
  )
}

function TaskActions({ task }: { task: TaskDTO }) {
  const [stopping, setStopping] = useState(false)
  const isRunning = task.status === 'RUNNING'
  return (
    <span className={styles.actions}>
      {task.service && (
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
        <button type="button" className={`${styles.btn} ${styles.danger}`} onClick={() => setStopping(true)}>
          Stop
        </button>
      )}
      {stopping && (
        <StopTaskDialog
          taskId={task.id}
          shortId={task.short_id.slice(0, 12)}
          serviceName={task.service?.name ?? null}
          serviceId={task.service?.id ?? null}
          desiredCount={null}
          onClose={() => setStopping(false)}
        />
      )}
    </span>
  )
}

const usageOf = (task: TaskDTO) => ({ cpu: `${formatVcpu(task.cpu)} vCPU`, memory: `${task.memory} MiB` })

/** One-line summary of a group: how many run where, or how long ago the first and last of the repeats stopped. */
function GroupSpread({ tasks }: { tasks: TaskDTO[] }) {
  if (tasks[0].status !== 'STOPPED') {
    const zones = new Set(tasks.map((task) => task.az)).size
    return <span className={styles.dim}>{`${tasks.length} running · ${zones} zone${zones === 1 ? '' : 's'}`}</span>
  }
  const times = tasks.flatMap((task) => (task.stopped_at ? [Date.parse(task.stopped_at)] : []))
  if (times.length === 0) return null
  return (
    <span className={styles.dim}>
      latest <Ago ts={Math.max(...times)} /> · oldest <Ago ts={Math.min(...times)} />
    </span>
  )
}

interface TasksTableProps {
  rows: TaskTableRow[]
  page: number
  onPageChange: (page: number) => void
  onToggleGroup: (groupKey: string) => void
  empty: string
}

/** The redesigned Tasks list: 50 rows per page, repeat groups as expandable rows. */
export function TasksTable({ rows, page, onPageChange, onToggleGroup, empty }: TasksTableProps) {
  const lead = (row: TaskTableRow) => (row.kind === 'group' ? row.tasks[0] : row.task)
  const columns: DataTableColumn<TaskTableRow>[] = [
    {
      key: 'task',
      header: 'Task',
      width: '120px',
      render: (row) =>
        row.kind === 'group' ? (
          <button type="button" className={styles.groupToggle} aria-expanded={row.open} onClick={() => onToggleGroup(row.groupKey)}>
            <span className={styles.chevron} aria-hidden="true">
              {row.open ? '▾' : '▸'}
            </span>
            {`×${row.tasks.length} tasks`}
          </button>
        ) : (
          <span className={`${styles.mono} ${row.nested ? styles.nested : ''}`} title={row.task.id}>
            {row.task.short_id.slice(0, 12)}
          </span>
        ),
    },
    { key: 'service', header: 'Service', width: 'minmax(0,1.1fr)', render: (row) => <ServiceCell task={lead(row)} /> },
    {
      key: 'status',
      header: 'Status',
      width: '220px',
      render: (row) => {
        const task = lead(row)
        const status = statusOf(task)
        return (
          <span className={styles.statusCell}>
            <StatusDot kind={status.kind}>{status.label}</StatusDot>
            {task.status === 'STOPPED' && <StopCauseChip cause={task.stop_cause} />}
          </span>
        )
      },
    },
    { key: 'health', header: 'Health', width: '76px', render: (row) => <HealthCell task={lead(row)} /> },
    {
      key: 'revision',
      header: 'Rev',
      width: '44px',
      render: (row) => <span className={styles.mono}>:{lead(row).revision}</span>,
    },
    {
      key: 'ip',
      header: 'IP · Subnet',
      width: '170px',
      render: (row) =>
        row.kind === 'group' ? null : (
          <span className={styles.stack}>
            <span className={styles.mono}>{row.task.private_ip}</span>
            <span className={styles.subMono}>
              {row.task.subnet_id} · {row.task.az}
            </span>
          </span>
        ),
    },
    {
      key: 'capacity',
      header: 'Capacity',
      width: '112px',
      render: (row) => (row.kind === 'group' ? null : <span className={styles.small}>{row.task.capacity}</span>),
    },
    {
      key: 'cpu',
      header: 'CPU',
      width: '76px',
      align: 'right',
      render: (row) => (row.kind === 'group' ? null : <span className={styles.mono}>{usageOf(row.task).cpu}</span>),
    },
    {
      key: 'memory',
      header: 'Memory',
      width: '80px',
      align: 'right',
      render: (row) => (row.kind === 'group' ? null : <span className={styles.mono}>{usageOf(row.task).memory}</span>),
    },
    {
      key: 'started',
      header: 'Started',
      width: '76px',
      render: (row) => {
        if (row.kind === 'group') return null
        return row.task.started_at ? <Ago ts={row.task.started_at} /> : <span className={styles.dim}>–</span>
      },
    },
    {
      key: 'reason',
      header: 'Stopped reason',
      width: 'minmax(0,2fr)',
      render: (row) => {
        if (row.kind === 'group') {
          const reason = row.tasks[0].stopped_reason
          return (
            <span className={styles.reason} title={reason}>
              {reason} <GroupSpread tasks={row.tasks} />
            </span>
          )
        }
        const { stopped_reason: reason, stopped_at: stoppedAt } = row.task
        return (
          <span className={styles.reason} title={reason}>
            {reason}
            {stoppedAt && (
              <span className={styles.dim}>
                {reason ? ' · ' : ''}
                <Ago ts={stoppedAt} />
              </span>
            )}
          </span>
        )
      },
    },
    {
      key: 'actions',
      header: '',
      width: '170px',
      align: 'right',
      render: (row) =>
        row.kind === 'group' ? (
          row.tasks[0].service ? (
            <span className={styles.actions}>
              <Link to={`/services/${row.tasks[0].service.id}/logs`} className={styles.btn}>
                Logs
              </Link>
            </span>
          ) : null
        ) : (
          <TaskActions task={row.task} />
        ),
    },
  ]

  return (
    <DataTable
      label="Tasks"
      columns={columns}
      rows={rows}
      rowKey={(row) => row.rowKey}
      rowStatus={(row) => stripeOf(lead(row))}
      minWidth={1450}
      pageSize={PAGE_SIZE}
      page={page}
      onPageChange={onPageChange}
      empty={empty}
    />
  )
}
