import { useState } from 'react'
import { useSearchParams } from 'react-router'
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
import { LANES, RANGES, formatEventTime, groupMarkers, logEvents, type MarkerGroup } from './model'
import { TimelineDetail } from './TimelineDetail'
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
    const target = data.events.find((event) => event.id === selectedId)
    if (target && !lanesOn[target.lane]) setLanesOn((current) => ({ ...current, [target.lane]: true }))
  }
  const from = data ? Date.parse(data.from) : 0
  const to = data ? Date.parse(data.to) : 0
  const withDate = to - from > DAY_MS

  const logLaneEvents = hasLogs && lanesOn.logs && data && logErrors.data ? logEvents(logErrors.data, to) : []
  const laneEvents: Record<TimelineLane, TimelineEvent[]> = { change: [], stop: [], alert: [], logs: logLaneEvents, access: [] }
  for (const event of data?.events ?? []) laneEvents[event.lane].push(event)

  const groups = Object.fromEntries(LANE_ORDER.map((lane) => [lane, groupMarkers(laneEvents[lane], from, to)])) as Record<TimelineLane, MarkerGroup[]>

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

  const selectedEvent = visibleEvents.find((event) => event.id === selectedId)

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
            isTrigger={selectedEvent.id === data?.incident?.trigger?.id}
            rollbackRevision={data?.incident?.rollback_revision ?? null}
          />
        ) : (
          <Panel title="Details">
            <p className={styles.hint}>Select an event to see the details.</p>
          </Panel>
        )}
      </div>
    </div>
  )
}
