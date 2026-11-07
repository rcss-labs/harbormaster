// Pure helpers the Timeline tab builds on: lane and range tables, log error events, marker grouping and axis ticks.
import type { TimelineEvent, TimelineLane, TimelineLogErrorsDTO, TimelineRange } from '../../../api/timeline'

export type { TimelineLane }

export const LANES: Record<TimelineLane, string> = {
  change: 'Changes',
  stop: 'Task stops',
  alert: 'Alerts',
  logs: 'Log errors',
  access: 'Terminal',
}

const HOUR_MS = 3_600_000
const DAY_MS = 24 * HOUR_MS

export const RANGES: { value: TimelineRange; label: string; ms: number }[] = [
  { value: '1h', label: '1 hour', ms: HOUR_MS },
  { value: '6h', label: '6 hours', ms: 6 * HOUR_MS },
  { value: '24h', label: '24 hours', ms: DAY_MS },
  { value: '7d', label: '7 days', ms: 7 * DAY_MS },
]

/** Markers closer than this share of the track width merge into one group. */
const GROUP_DISTANCE_PCT = 2.5
/** A typical rate needs this many counted, uncapped buckets to mean anything. */
const MIN_BUCKETS_FOR_TYPICAL_RATE = 6

export interface MarkerGroup {
  pct: number
  items: TimelineEvent[]
  severity: TimelineEvent['severity']
}

function bucketPeriodText(bucketSeconds: number): string {
  if (bucketSeconds >= 3600) {
    const hours = Math.round(bucketSeconds / 3600)
    return `${hours} ${hours === 1 ? 'hour' : 'hours'}`
  }
  const minutes = Math.round(bucketSeconds / 60)
  return `${minutes} ${minutes === 1 ? 'minute' : 'minutes'}`
}

function median(values: number[]): number {
  const sorted = [...values].sort((a, b) => a - b)
  const middle = Math.floor(sorted.length / 2)
  return sorted.length % 2 === 1 ? sorted[middle] : (sorted[middle - 1] + sorted[middle]) / 2
}

/** One warn event per bucket that has error lines. `to` (ms) keeps the open bucket from landing in the future. */
export function logEvents(data: TimelineLogErrorsDTO, to: number): TimelineEvent[] {
  const period = bucketPeriodText(data.bucket_seconds)
  const counted = data.buckets.filter((bucket) => bucket.known && !bucket.capped).map((bucket) => bucket.count)
  const typicalRate = counted.length >= MIN_BUCKETS_FOR_TYPICAL_RATE ? Math.round(median(counted)) : null

  const events: TimelineEvent[] = []
  for (const bucket of data.buckets) {
    if (!bucket.known || bucket.count <= 0) continue
    const start = Date.parse(bucket.start)
    const end = Math.min(start + data.bucket_seconds * 1000, to)
    const lines = `${bucket.count} error ${bucket.count === 1 ? 'line' : 'lines'}`
    const detail = [
      bucket.top_message ? `Most frequent: "${bucket.top_message}".` : '',
      typicalRate === null ? '' : `Usually around ${typicalRate} per ${period}.`,
    ]
      .filter(Boolean)
      .join(' ')
    events.push({
      id: `log:${start}`,
      lane: 'logs',
      severity: 'warn',
      title: `${bucket.capped ? 'At least ' : ''}${lines} in ${period}`,
      actor: 'CloudWatch Logs',
      detail,
      op: '',
      task_id: '',
      at: new Date(end).toISOString(),
      evidence_at: null,
      diff: [],
      from_revision: null,
      to_revision: null,
    })
  }
  return events
}

/** Walks the events oldest to newest and joins each to the previous group when it sits within 2.5% of that group's first marker. */
export function groupMarkers(events: TimelineEvent[], from: number, to: number): MarkerGroup[] {
  const span = to - from
  const positioned = events
    .map((event) => {
      const raw = span > 0 ? ((Date.parse(event.at) - from) / span) * 100 : 0
      return { event, at: Date.parse(event.at), pct: Math.min(100, Math.max(0, raw)) }
    })
    .sort((a, b) => a.at - b.at)

  const groups: MarkerGroup[] = []
  for (const { event, pct } of positioned) {
    const previous = groups.at(-1)
    if (previous && pct - previous.pct < GROUP_DISTANCE_PCT - 1e-9) {
      previous.items.push(event)
      if (event.severity === 'bad') previous.severity = 'bad'
    } else {
      groups.push({ pct, items: [event], severity: event.severity })
    }
  }
  return groups
}

/** The shortest range that still reaches back to `at`, or null when it is older than 7 days. */
export function smallestRangeContaining(at: number, now: number): TimelineRange | null {
  const age = now - at
  return RANGES.find((range) => age <= range.ms)?.value ?? null
}

function pad(value: number): string {
  return String(value).padStart(2, '0')
}

/** Ticks at 0, 25, 50, 75 and 100% in the browser's zone; the right edge reads `now` and ranges over a day show dates. */
export function axisTicks(from: number, to: number): { pct: number; label: string }[] {
  const showDate = to - from > DAY_MS
  return [0, 25, 50, 75, 100].map((pct) => {
    if (pct === 100) return { pct, label: 'now' }
    const date = new Date(from + ((to - from) * pct) / 100)
    const time = `${pad(date.getHours())}:${pad(date.getMinutes())}`
    const label = showDate ? `${date.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })} ${time}` : time
    return { pct, label }
  })
}

/** Clock time in the browser's zone; ranges over a day add the date. */
export function formatEventTime(iso: string, withDate: boolean): string {
  const date = new Date(iso)
  const time = `${pad(date.getHours())}:${pad(date.getMinutes())}`
  return withDate ? `${date.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })} ${time}` : time
}
