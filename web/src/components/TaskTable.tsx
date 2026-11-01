import { useState } from 'react'
import { Link } from 'react-router'
import type { TaskDTO } from '../api/types'
import { Ago } from './Ago'
import { Pill, type PillKind } from './Pill'
import { StopTaskDialog } from './StopTaskDialog'
import { Table, tableClass } from './Table'
import styles from './TaskTable.module.css'

interface TaskTableProps {
  items: TaskDTO[]
  /** Adds the Service column (the Tasks page); the service Tasks tab leaves it out. */
  withService: boolean
  /** Cursor for the next page; "Load more" shows only when set. */
  next?: string
  onMore(): void
  /** Live usage in percent of the reserved size, keyed by the task's short id (the id CloudWatch reports). */
  utilisation?: Record<string, { cpu: number | null; mem: number | null }>
  /** The service's desired count, for the Stop dialog's copy; left out on the global list. */
  desiredCount?: number | null
}

const PROGRESS_STATUSES = ['PROVISIONING', 'PENDING', 'ACTIVATING']
const titleCase = (status: string) => status.charAt(0) + status.slice(1).toLowerCase()

/** vCPU from ECS CPU units: up to two decimals, trailing zeros dropped (256 → 0.25, 1024 → 1). */
const formatVcpu = (cpuUnits: string) => String(+(Number(cpuUnits) / 1024).toFixed(2))

function statusPill(task: TaskDTO): { kind: PillKind; label: string } {
  if (task.status === 'RUNNING') return { kind: 'ok', label: 'Running' }
  if (PROGRESS_STATUSES.includes(task.status)) return { kind: 'info', label: titleCase(task.status) }
  if (task.status === 'STOPPED') {
    if (task.exit_code !== null && task.exit_code !== 0) return { kind: 'bad', label: `Stopped (exit ${task.exit_code})` }
    return { kind: 'mute', label: 'Stopped' }
  }
  // DEACTIVATING, STOPPING, DEPROVISIONING and anything new.
  return { kind: 'mute', label: titleCase(task.status) }
}

function HealthCell({ task }: { task: TaskDTO }) {
  if (task.status !== 'RUNNING') return <span className={styles.muted}>–</span>
  if (task.health === 'HEALTHY') return <span className={styles.healthy}>Healthy</span>
  if (task.health === 'UNHEALTHY') return <span className={styles.unhealthy}>Unhealthy</span>
  return <span className={styles.muted}>No check</span>
}

/** Row stripe: a stopped task with an error exit is red; a running task failing its health check is amber. */
function stripeFor(task: TaskDTO): string | undefined {
  if (task.status === 'STOPPED' && task.exit_code !== null && task.exit_code !== 0) return tableClass.stripe('bad')
  if (task.status === 'RUNNING' && task.health === 'UNHEALTHY') return tableClass.stripe('warn')
  return undefined
}

function ServiceCell({ task }: { task: TaskDTO }) {
  if (task.service) {
    return (
      <>
        <Link to={`/services/${task.service.id}/tasks`} className={styles.accent}>
          {task.service.name}
        </Link>
        <div className={styles.sub}>{task.cluster.name}</div>
      </>
    )
  }
  return (
    <>
      <span>{task.group}</span>
      <div className={styles.sub}>Standalone · started by {task.started_by}</div>
    </>
  )
}

/** "34% of 512 MiB" when usage is known, else the reserved size alone. */
const usageOrReserved = (usedPercent: number | null | undefined, reserved: string) =>
  usedPercent === null || usedPercent === undefined ? reserved : `${Math.round(usedPercent)}% of ${reserved}`

function TaskRow({
  task,
  withService,
  usage,
  desiredCount,
}: {
  task: TaskDTO
  withService: boolean
  usage?: { cpu: number | null; mem: number | null }
  desiredCount: number | null
}) {
  const pill = statusPill(task)
  const isRunning = task.status === 'RUNNING'
  const [stopping, setStopping] = useState(false)
  return (
    <tr className={stripeFor(task)}>
      <td className={styles.mono} title={task.id}>
        {task.short_id.slice(0, 12)}
      </td>
      {withService && (
        <td>
          <ServiceCell task={task} />
        </td>
      )}
      <td>
        <Pill kind={pill.kind}>{pill.label}</Pill>
      </td>
      <td>
        <HealthCell task={task} />
      </td>
      <td className={styles.mono}>:{task.revision}</td>
      <td>
        <span className={styles.mono}>{task.private_ip}</span>
        <div className={styles.sub}>
          {task.subnet_id} · {task.az}
        </div>
      </td>
      <td className={styles.small}>{task.capacity}</td>
      <td className={tableClass.num}>{usageOrReserved(usage?.cpu, `${formatVcpu(task.cpu)} vCPU`)}</td>
      <td className={tableClass.num}>{usageOrReserved(usage?.mem, `${task.memory} MiB`)}</td>
      <td className={styles.muted}>{task.started_at ? <Ago ts={task.started_at} /> : '–'}</td>
      <td className={styles.reason}>
        {task.stopped_reason}
        {task.stopped_at && (
          <>
            {task.stopped_reason ? ' ' : ''}
            <span className={styles.muted}>
              · <Ago ts={task.stopped_at} />
            </span>
          </>
        )}
      </td>
      <td className={styles.actions}>
        {isRunning && task.can.terminal && task.exec_enabled && (
          <Link to={terminalPath(task)} className={styles.btn}>
            Terminal
          </Link>
        )}{' '}
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
            desiredCount={desiredCount}
            onClose={() => setStopping(false)}
          />
        )}
      </td>
    </tr>
  )
}

/** Service tasks open the service's Terminal tab; standalone tasks have no service and get their own page. */
const terminalPath = (task: TaskDTO) =>
  task.service
    ? `/services/${task.service.id}/terminal?task=${encodeURIComponent(task.id)}`
    : `/tasks/${encodeURIComponent(task.short_id)}/terminal`

/** Task rows as in the mockup's taskRow(); Terminal deep-links into the service's Terminal tab. */
export function TaskTable({ items, withService, next, onMore, utilisation, desiredCount = null }: TaskTableProps) {
  return (
    <div className={styles.panel}>
      <Table>
        <thead>
          <tr>
            <th>Task</th>
            {withService && <th>Service</th>}
            <th>Status</th>
            <th>Health</th>
            <th>Revision</th>
            <th>Subnet / IP</th>
            <th>Capacity</th>
            <th className={tableClass.num}>CPU</th>
            <th className={tableClass.num}>Memory</th>
            <th>Started</th>
            <th>Stopped reason</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {items.map((task) => (
            <TaskRow
              key={task.id}
              task={task}
              withService={withService}
              usage={utilisation?.[task.short_id]}
              desiredCount={desiredCount}
            />
          ))}
        </tbody>
      </Table>
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
