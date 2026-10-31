import { Link } from 'react-router'
import type { TaskDTO } from '../api/types'
import { fmtNum } from '../charts/format'
import { Ago } from './Ago'
import { Pill, type PillKind } from './Pill'
import { Table, tableClass } from './Table'
import styles from './TaskTable.module.css'

interface TaskTableProps {
  items: TaskDTO[]
  /** Adds the Service column (the Tasks page); the service Tasks tab leaves it out. */
  withService: boolean
  /** Cursor for the next page; "Load more" shows only when set. */
  next?: string
  onMore(): void
}

const PROGRESS_STATUSES = ['PROVISIONING', 'PENDING', 'ACTIVATING']
const titleCase = (status: string) => status.charAt(0) + status.slice(1).toLowerCase()

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

function TaskRow({ task, withService }: { task: TaskDTO; withService: boolean }) {
  const pill = statusPill(task)
  const isRunning = task.status === 'RUNNING'
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
      <td className={tableClass.num}>{fmtNum(Number(task.cpu) / 1024)} vCPU</td>
      <td className={tableClass.num}>{task.memory} MiB</td>
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
        {isRunning && task.can.terminal && (
          <button type="button" className={styles.btn} disabled title="Available in phase 4">
            Terminal
          </button>
        )}{' '}
        {isRunning && task.can.operate && (
          <button type="button" className={`${styles.btn} ${styles.danger}`} disabled title="Available in phase 2">
            Stop
          </button>
        )}
      </td>
    </tr>
  )
}

/** Task rows as in the mockup's taskRow(); buttons are placeholders until phases 2 and 4. */
export function TaskTable({ items, withService, next, onMore }: TaskTableProps) {
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
            <TaskRow key={task.id} task={task} withService={withService} />
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
