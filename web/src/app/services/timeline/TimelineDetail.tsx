import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router'
import { useRevisionDefinition } from '../../../api/deploy'
import { ApiError } from '../../../api/client'
import { useTimelineTask, type TimelineEvent } from '../../../api/timeline'
import type { ServiceDTO } from '../../../api/types'
import { LockNote } from '../../../components/LockNote'
import { Panel } from '../../../components/Panel'
import { Pill } from '../../../components/Pill'
import { Skeleton } from '../../../components/Skeleton'
import { useToast } from '../../../components/Toast'
import { useTaskPanel } from '../../tasks/useTaskPanel'
import { LANES, formatEventTime } from './model'
import { revisionDiff, type DiffRow } from './revisionDiff'
import styles from './timeline.module.css'

const LOGS_WINDOW_AFTER_EVENT_MS = 60_000

interface TimelineDetailProps {
  serviceId: string
  service: ServiceDTO
  event: TimelineEvent
  isTrigger: boolean
  /** Rendered by the rollback task; accepted here so the tab can pass them through. */
  rollbackRevision?: number | null
  onRollback?: () => void
}

/** Where the Logs tab should end for this event: a little after it, never in the future; a log bucket already ends where it should. */
function logsBefore(event: TimelineEvent): string {
  if (event.lane === 'logs') return event.at
  return new Date(Math.min(Date.parse(event.at) + LOGS_WINDOW_AFTER_EVENT_MS, Date.now())).toISOString()
}

function DiffGrid({ rows }: { rows: DiffRow[] }) {
  return (
    <div className={styles.diff} role="group" aria-label="Changes">
      {rows.map((row) => (
        <div key={row.label} className={styles.diffRow}>
          <span className={styles.diffLabel}>{row.label}</span>
          <span className={styles.diffFrom}>{row.from}</span>
          <span className={styles.diffTo}>{row.to}</span>
        </div>
      ))}
    </div>
  )
}

/** The diff under an event: the server's rows plus, for a revision change, the two definitions compared here. */
function EventDiff({ serviceId, event }: { serviceId: string; event: TimelineEvent }) {
  const before = useRevisionDefinition(serviceId, event.from_revision !== null && event.to_revision !== null ? event.from_revision : null)
  const after = useRevisionDefinition(serviceId, event.from_revision !== null && event.to_revision !== null ? event.to_revision : null)
  const compares = event.from_revision !== null && event.to_revision !== null

  const failed = compares && (before.isError || after.isError)
  if (compares && !failed && (before.isPending || after.isPending)) return <Skeleton lines={3} label="Loading the diff" />
  const rows = [...event.diff, ...(compares && before.data && after.data ? revisionDiff(before.data.definition, after.data.definition) : [])]
  return (
    <>
      {failed && (
        <p className={styles.hint}>
          The diff could not be loaded. <Link to={`/services/${serviceId}/task-definition`}>Task definition</Link>
        </p>
      )}
      {rows.length > 0 && <DiffGrid rows={rows} />}
    </>
  )
}

/** A stopped task may be gone from the Tasks list, so it is fetched on its own and shown in the usual drawer. */
function OpenTaskButton({ serviceId, taskId, onOpen }: { serviceId: string; taskId: string; onOpen: (task: NonNullable<ReturnType<typeof useTimelineTask>['data']>) => void }) {
  const toast = useToast()
  // Each click is a request; the ref remembers the last one answered so a refetch does not open the drawer again.
  const [request, setRequest] = useState(0)
  const answeredRequest = useRef(0)
  const task = useTimelineTask(serviceId, request > 0 ? taskId : null)

  useEffect(() => {
    if (request === 0 || request === answeredRequest.current) return
    if (task.data) {
      answeredRequest.current = request
      onOpen(task.data)
    } else if (task.error) {
      answeredRequest.current = request
      toast.show(task.error instanceof ApiError && task.error.status === 404 ? 'This task is no longer stored.' : 'The task could not be loaded.', 'warn')
    }
  }, [request, task.data, task.error, onOpen, toast])

  return (
    <button type="button" className={styles.action} onClick={() => setRequest((count) => count + 1)}>
      Open task
    </button>
  )
}

/** Everything known about one timeline event, with the links onward to logs and the task. */
export function TimelineDetail({ serviceId, service, event, isTrigger }: TimelineDetailProps) {
  const { onOpen, panel } = useTaskPanel([], service.desired)
  const logsReason = `You need Logs on ${service.tag.name}`

  return (
    <Panel title="Details">
      <div className={styles.detailBody}>
        <div className={styles.detailHead}>
          <strong>{event.title}</strong>
          {isTrigger && <Pill kind="warn">Likely trigger</Pill>}
        </div>
        <p className={styles.hint}>
          {LANES[event.lane]} · {formatEventTime(event.at, true)}
          {event.actor && ` · ${event.actor}`}
        </p>
        {event.evidence_at && <p className={styles.hint}>{`Evidence from ${formatEventTime(event.evidence_at, true)}`}</p>}
        {event.detail && <p>{event.detail}</p>}
        <EventDiff key={event.id} serviceId={serviceId} event={event} />
        <div className={styles.actions}>
          {service.can.logs ? (
            <Link className={styles.action} to={`/services/${serviceId}/logs?before=${encodeURIComponent(logsBefore(event))}`}>
              Open logs at this time
            </Link>
          ) : (
            <span className={`${styles.action} ${styles.disabled}`} aria-disabled="true" title={logsReason}>
              Open logs at this time
            </span>
          )}
          {event.lane === 'stop' && event.task_id && <OpenTaskButton key={event.task_id} serviceId={serviceId} taskId={event.task_id} onOpen={onOpen} />}
        </div>
        {!service.can.logs && <LockNote kind="why">{logsReason}</LockNote>}
      </div>
      {panel}
    </Panel>
  )
}
