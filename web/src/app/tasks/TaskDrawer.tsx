import { Link } from 'react-router'
import { useExecTargets } from '../../api/exec'
import { useTaskLogs } from '../../api/observe'
import type { TaskDTO } from '../../api/types'
import { Ago } from '../../components/Ago'
import { Drawer } from '../../components/Drawer'
import { Icon } from '../../components/Icon'
import { usePhone } from '../../components/useNarrow'
import { KV } from '../../components/KV'
import { Skeleton } from '../../components/Skeleton'
import { TaskStatusCell, formatVcpu, terminalPath } from '../../components/TaskTable'
import styles from './TaskDrawer.module.css'

interface TaskDrawerProps {
  task: TaskDTO
  onClose(): void
  /** The host opens the Stop confirm dialog; the drawer only asks. */
  onStop(task: TaskDTO): void
}

const LOG_LINES = 20

const clock = (iso: string) => new Date(iso).toLocaleTimeString([], { hour12: false })

/** Newest lines of this task only (the server restricts the read to the task's streams). Standalone tasks have no service. */
function RecentLogs({ task }: { task: TaskDTO }) {
  const serviceId = task.service?.id
  const logs = useTaskLogs(serviceId ?? '', task.short_id, { enabled: Boolean(serviceId), limit: LOG_LINES })
  if (!serviceId) return <p className={styles.note}>No service, so no logs here.</p>
  if (logs.isError) return <p className={styles.note}>{logs.error instanceof Error ? logs.error.message : 'The logs could not be loaded.'}</p>
  if (logs.isPending) return <Skeleton lines={3} label="Loading log lines" />
  const lines = logs.data.lines.slice(-LOG_LINES)
  if (lines.length === 0) return <p className={styles.note}>No log lines.</p>
  return (
    <div className={styles.code}>
      {lines.map((line, index) => (
        <div key={`${line.t}-${index}`}>
          <span className={styles.muted}>{clock(line.t)}</span> {line.message}
        </div>
      ))}
    </div>
  )
}

/** Containers of a running task from its ECS Exec targets; a stopped task has none to ask. */
function Containers({ task }: { task: TaskDTO }) {
  const running = task.status === 'RUNNING'
  const targets = useExecTargets(running ? task.id : undefined)
  if (!running) return <p className={styles.note}>Containers are listed while the task is running.</p>
  if (targets.isError) return <p className={styles.note}>The containers could not be loaded.</p>
  if (!targets.data) return <Skeleton lines={2} label="Loading containers" />
  return (
    <div role="table" aria-label="Containers" className={styles.containers}>
      <div role="row" className={styles.containerRow}>
        <span role="columnheader" className={styles.muted}>
          Name
        </span>
        <span role="columnheader" className={styles.muted}>
          Exec agent
        </span>
      </div>
      {targets.data.containers.map((container) => (
        <div role="row" key={container.name} className={styles.containerRow}>
          <span role="cell" className={styles.mono}>
            {container.name}
          </span>
          <span role="cell">{container.exec_agent}</span>
        </div>
      ))}
    </div>
  )
}

/** Friendly reason first (the API's reason group), then the raw ECS message in a code block. */
function StopReason({ task }: { task: TaskDTO }) {
  if (task.status !== 'STOPPED') return null
  const friendly = task.stop_reason_group || task.stopped_reason || 'Not recorded'
  return (
    <div className={styles.reason}>
      <div className={styles.strong}>{friendly}</div>
      <div className={styles.muted}>Reported by ECS</div>
      <pre className={styles.raw}>{task.stopped_reason || 'No stop message was recorded.'}</pre>
    </div>
  )
}

/** Right-hand details for one task: identity, why it stopped, containers, recent log lines and the row actions. */
export function TaskDrawer({ task, onClose, onStop }: TaskDrawerProps) {
  const phone = usePhone()
  const isRunning = task.status === 'RUNNING'
  const shortId = task.short_id.slice(0, 12)
  const owner = task.service ? task.service.name : 'Standalone'
  const where = task.service ? task.cluster.name : `Started by ${task.started_by}`
  return (
    <Drawer open wide onClose={onClose} title={<span className={styles.mono}>{shortId}</span>} sub={`${owner} · ${where}`}>
      <div className={styles.stack}>
        <div className={styles.actions}>
          <TaskStatusCell task={task} />
          {task.service && (
            <Link to={`/services/${task.service.id}/logs`} className={styles.btn}>
              <Icon name="logs" />
              Logs
            </Link>
          )}
          {isRunning && task.can.terminal && task.exec_enabled && (
            <Link to={terminalPath(task)} className={styles.btn}>
              <Icon name="term" />
              Terminal
            </Link>
          )}
          {isRunning && task.can.terminal && !task.exec_enabled && (
            <span className={`${styles.btn} ${styles.off}`} aria-disabled="true" title="ECS Exec is off for this task">
              <Icon name="term" />
              Terminal
            </span>
          )}
          {isRunning && task.can.terminal && !task.exec_enabled && phone && <span className={styles.note}>ECS Exec is off for this task</span>}
          {isRunning && task.can.operate && (
            <button type="button" className={`${styles.btn} ${styles.danger}`} onClick={() => onStop(task)}>
              <Icon name="stop" />
              Stop task
            </button>
          )}
        </div>

        <StopReason task={task} />

        <section className={styles.section}>
          <h3>Identity</h3>
          <KV
            rows={[
              ['Task ID', <span className={styles.mono}>{task.id}</span>],
              ['ARN', <span className={styles.mono}>{task.arn}</span>],
              ['Service', owner],
              ['Cluster', `${task.cluster.name} · ${task.region}`],
              ['Definition', <span className={styles.mono}>{`${task.family}:${task.revision}`}</span>],
              ['Capacity', <span className={styles.mono}>{task.capacity}</span>],
              ['Size', `${formatVcpu(task.cpu)} vCPU · ${task.memory} MiB`],
              ['Zone', <span className={styles.mono}>{task.az}</span>],
              ['IP · Subnet', <span className={styles.mono}>{`${task.private_ip} · ${task.subnet_id}`}</span>],
              ['Health', task.health === 'UNKNOWN' ? '–' : task.health.charAt(0) + task.health.slice(1).toLowerCase()],
              ['Started', task.started_at ? <Ago ts={task.started_at} /> : '–'],
              ['Stopped', task.stopped_at ? <Ago ts={task.stopped_at} /> : '–'],
            ]}
          />
        </section>

        <section className={styles.section}>
          <h3>Containers</h3>
          <Containers task={task} />
        </section>

        <section className={styles.section}>
          <div className={styles.spread}>
            <h3>Recent log lines</h3>
            {task.service && <Link to={`/services/${task.service.id}/logs`}>Open logs</Link>}
          </div>
          <RecentLogs task={task} />
        </section>
      </div>
    </Drawer>
  )
}
