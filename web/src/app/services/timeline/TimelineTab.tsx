import { useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { useTimeline, useTimelineLogErrors, type TimelineEvent, type TimelineLane, type TimelineRange } from '../../../api/timeline'
import type { ServiceDTO } from '../../../api/types'
import { EmptyState } from '../../../components/EmptyState'
import { LockNote } from '../../../components/LockNote'
import { Panel } from '../../../components/Panel'
import { Pill, type PillKind } from '../../../components/Pill'
import { SegmentedFilter } from '../../../components/Seg'
import { Table } from '../../../components/Table'
import { Toggle } from '../../../components/Toggle'
import { usePhone } from '../../../components/usePhone'
import { queryPanelError } from '../panelError'
import { LANES, PHONE_GROUP_DISTANCE_PCT, RANGES, formatEventTime, groupMarkers, logEvents, smallestRangeContaining, type MarkerGroup } from './model'
import { TimelineBanner } from './TimelineBanner'
import { TimelineDetail } from './TimelineDetail'
import { useEventDiff } from './useEventDiff'
import { TimelineLanes, type LaneRowState } from './TimelineLanes'
import styles from './timeline.module.css'

const LANE_ORDER = Object.keys(LANES) as TimelineLane[]
const DAY_MS = 24 * 3_600_000
const DEFAULT_RANGE: TimelineRange = '1h'
const PARTIAL_LOGS_MESSAGE = 'Some periods could not be read.'
const TRUNCATED_NOTE = 'Showing the newest 500.'

const pillKind: Record<TimelineEvent['severity'], PillKind> = { bad: 'bad', warn: 'warn', info: 'info', change: 'info' }

const parseRange = (value: string | null): TimelineRange => RANGES.find((range) => range.value === value)?.value ?? DEFAULT_RANGE

/** The service timeline: a lane per kind of event over the chosen range, and the same events as a list. */
export function TimelineTab({ serviceId, service }: { serviceId: string; service: ServiceDTO }) {
  const phone = usePhone()
  const [searchParams, setSearchParams] = useSearchParams()
  const navigate = useNavigate()
  const range = parseRange(searchParams.get('range'))
  const [lanesOn, setLanesOn] = useState<Record<TimelineLane, boolean>>({ change: true, stop: true, alert: true, logs: true, access: true })

  const hasLogs = service.can.logs
  const logsLockReason = `You need Logs on ${service.tag.name}`
  const timeline = useTimeline(serviceId, range)
  const logErrors = useTimelineLogErrors(serviceId, range, hasLogs && lanesOn.logs)

  const updateParams = (change: (params: URLSearchParams) => void) =>
    setSearchParams(
      (previous) => {
        const next = new URLSearchParams(previous)
        change(next)
        return next
      },
      { replace: true },
    )

  const data = timeline.data
  // Without ?event= the incident's trigger is shown, so the first look at a failing service lands on its likely cause.
  const selectedId = searchParams.get('event') ?? data?.incident?.trigger?.id ?? null
  const [syncedSelection, setSyncedSelection] = useState<string | null>(null)
  if (data && selectedId !== syncedSelection) {
    setSyncedSelection(selectedId)
    // Log events are built from the log-errors query, which is off with the lane, so their lane comes from the id.
    const lane: TimelineLane | undefined = selectedId?.startsWith('log:') ? (hasLogs ? 'logs' : undefined) : data.events.find((event) => event.id === selectedId)?.lane
    if (lane && !lanesOn[lane]) setLanesOn((current) => ({ ...current, [lane]: true }))
  }
  const from = data ? Date.parse(data.from) : 0
  const to = data ? Date.parse(data.to) : 0
  const withDate = to - from > DAY_MS

  const logLaneEvents = hasLogs && lanesOn.logs && data && logErrors.data ? logEvents(logErrors.data, to) : []
  const laneEvents: Record<TimelineLane, TimelineEvent[]> = { change: [], stop: [], alert: [], logs: logLaneEvents, access: [] }
  for (const event of data?.events ?? []) laneEvents[event.lane].push(event)

  const groups = Object.fromEntries(LANE_ORDER.map((lane) => [lane, groupMarkers(laneEvents[lane], from, to, phone ? PHONE_GROUP_DISTANCE_PCT : undefined)])) as Record<TimelineLane, MarkerGroup[]>

  const laneStates = Object.fromEntries(
    LANE_ORDER.map((lane): [TimelineLane, LaneRowState] => {
      if (lane === 'logs') return ['logs', logLaneState()]
      return [lane, { on: lanesOn[lane] }]
    }),
  ) as Record<TimelineLane, LaneRowState>

  function logLaneState(): LaneRowState {
    if (!hasLogs) return { on: false, message: logsLockReason }
    if (!lanesOn.logs) return { on: false }
    if (logErrors.isPending) return { on: true, loading: true }
    const retry = () => void logErrors.refetch()
    if (logErrors.isError) return { on: true, message: 'Log errors could not be read.', onRetry: retry }
    const { state, message } = logErrors.data
    if (state === 'unavailable') return { on: true, message, onRetry: retry }
    if (state === 'no_logs') return { on: true, message }
    return { on: true }
  }

  // Notes sit in a list below the lanes: inside a track they would cover the markers they describe.
  const laneNotes = LANE_ORDER.filter((lane) => lanesOn[lane]).flatMap((lane): { lane: TimelineLane; text: string }[] => {
    if (lane === 'logs') return hasLogs && logErrors.data?.state === 'partial' ? [{ lane, text: PARTIAL_LOGS_MESSAGE }] : []
    const info = data?.lanes.find((candidate) => candidate.lane === lane)
    const text = [info?.note, info?.truncated ? TRUNCATED_NOTE : ''].filter(Boolean).join(' ')
    return text ? [{ lane, text }] : []
  })

  const anyLaneOn = LANE_ORDER.some((lane) => lanesOn[lane] && (lane !== 'logs' || hasLogs))
  const visibleEvents = LANE_ORDER.flatMap((lane) => (lanesOn[lane] ? laneEvents[lane] : []))
    .sort((a, b) => Date.parse(b.at) - Date.parse(a.at))

  const incident = data?.incident ?? null
  const trigger = incident?.trigger ?? null
  // A trigger older than the range, or on a lane that is off, is not in the lists but still has its details.
  const triggerOutsideRange = trigger !== null && data !== undefined && !data.events.some((event) => event.id === trigger.id)
  const selectedEvent = visibleEvents.find((event) => event.id === selectedId) ?? (triggerOutsideRange && trigger.id === selectedId ? trigger : undefined)
  // The server computes the incident over 7 days; it is only relevant when its last failure is inside the range.
  const showBanner = incident !== null && data !== undefined && Date.parse(incident.last_failure_at) >= from
  const triggerDiff = useEventDiff(serviceId, showBanner ? trigger : null).rows

  const showTheChange = () => {
    if (!trigger) return
    const needed = smallestRangeContaining(Date.parse(trigger.at), to)
    const neededIndex = needed === null ? -1 : RANGES.findIndex((candidate) => candidate.value === needed)
    const currentIndex = RANGES.findIndex((candidate) => candidate.value === range)
    // Never narrows the range, and leaves it alone when the trigger is older than the longest one.
    if (!lanesOn[trigger.lane]) setLanesOn((current) => ({ ...current, [trigger.lane]: true }))
    // One update for both: React Router's functional form does not queue, so a second call would drop the first.
    updateParams((params) => {
      if (neededIndex > currentIndex) params.set('range', RANGES[neededIndex].value)
      params.set('event', trigger.id)
    })
  }

  const select = (id: string, lane: TimelineLane) => {
    if (!lanesOn[lane]) setLanesOn((current) => ({ ...current, [lane]: true }))
    updateParams((params) => params.set('event', id))
  }

  const toggles = (
    <div className={styles.toggles} data-testid="timeline-toggles">
      {LANE_ORDER.map((lane) =>
        lane === 'logs' && !hasLogs ? (
          <span key={lane} title={logsLockReason}>
            <Toggle checked={false} onChange={() => {}} disabled>
              {LANES[lane]}
            </Toggle>
          </span>
        ) : (
          <Toggle key={lane} checked={lanesOn[lane]} onChange={(checked) => setLanesOn((current) => ({ ...current, [lane]: checked }))}>
            {LANES[lane]}
          </Toggle>
        ),
      )}
    </div>
  )

  return (
    <>
      {showBanner && <TimelineBanner incident={incident} triggerDiff={triggerDiff} onShowChange={showTheChange} />}
      <div className={`${styles.split} ${phone ? styles.stack : ''}`.trim()} data-testid="timeline-split">
        <div className={styles.list}>
          <Panel
            title="Timeline"
            toolbar={
              <>
                <SegmentedFilter
                  label="Range"
                  value={range}
                  onChange={(next) => updateParams((params) => params.set('range', next))}
                  options={RANGES.map(({ value, label }) => ({ value, label }))}
                />
                {toggles}
              </>
            }
            loading={timeline.isPending}
            error={queryPanelError(timeline, 'The timeline could not be read.')}
          >
            {!hasLogs && <LockNote kind="why">{logsLockReason}</LockNote>}
            {anyLaneOn ? (
              <TimelineLanes
                groups={groups}
                from={from}
                to={to}
                selectedId={selectedId}
                laneStates={laneStates}
                onSelect={(group) => select(group.items[0].id, group.items[0].lane)}
              />
            ) : (
              <>
                <EmptyState>Turn on at least one lane.</EmptyState>
                {!hasLogs && !phone && <p className={styles.hint}>{logsLockReason}</p>}
              </>
            )}
          </Panel>
          {anyLaneOn && laneNotes.length > 0 && (
            <ul className={styles.laneNotes} aria-label="Lane notes">
              {laneNotes.map(({ lane, text }) => (
                <li key={lane}>{`${LANES[lane]}: ${text}`}</li>
              ))}
            </ul>
          )}
          {data && anyLaneOn && (
            <Panel title="Events" sub={<span className={styles.count}>{visibleEvents.length} events in range</span>} flush>
              {visibleEvents.length === 0 ? (
                <EmptyState>No events in this range.</EmptyState>
              ) : (
                <Table>
                  <thead>
                    <tr>
                      <th>Time</th>
                      <th>Lane</th>
                      <th>Event</th>
                      <th>Actor</th>
                    </tr>
                  </thead>
                  <tbody>
                    {visibleEvents.map((event) => (
                      <tr key={event.id} className={event.id === selectedId ? styles.selectedRow : undefined}>
                        <td className={styles.time}>{formatEventTime(event.at, withDate)}</td>
                        <td>
                          <Pill kind={pillKind[event.severity]}>{LANES[event.lane]}</Pill>
                        </td>
                        <td>
                          <button type="button" className={styles.eventTitle} onClick={() => select(event.id, event.lane)}>
                            {event.title}
                          </button>
                        </td>
                        <td>{event.actor}</td>
                      </tr>
                    ))}
                  </tbody>
                </Table>
              )}
            </Panel>
          )}
        </div>
        <div className={styles.detail}>
          {selectedEvent ? (
            <TimelineDetail
              serviceId={serviceId}
              service={service}
              event={selectedEvent}
              isTrigger={selectedEvent.id === trigger?.id}
              rollbackRevision={incident?.trigger_unknown ? null : (incident?.rollback_revision ?? null)}
              onRollback={() => incident?.rollback_revision != null && navigate(`/services/${serviceId}/deploy?revision=${incident.rollback_revision}&review=1`)}
            />
          ) : (
            <Panel title="Details">
              <p className={styles.hint}>Select an event to see the details.</p>
            </Panel>
          )}
        </div>
      </div>
    </>
  )
}
