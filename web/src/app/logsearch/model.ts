// Pure helpers for the Log search page: the form and its URL, validation, the histogram, hit counts, cost and Logs tab links.
import type { LogSearchRange } from '../../api/logSearch'

const SECOND = 1000
const MINUTE = 60 * SECOND
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

export const MAX_SERVICES = 20
export const MAX_RANGE_MS = 7 * DAY
/** The server allows an end a minute ahead of its clock; the form does the same. */
const FUTURE_SLACK_MS = MINUTE
const MAX_BUCKETS = 60
const CUSTOM_BUCKET_SIZES = [30 * SECOND, MINUTE, 5 * MINUTE, 15 * MINUTE, 30 * MINUTE, HOUR, 3 * HOUR, 6 * HOUR, 12 * HOUR]
/** $0.005 per GB scanned, GB = 2^30 bytes. */
const COST_PER_GB_USD = 0.005
const GB = 2 ** 30

export const RANGES: { value: LogSearchRange; label: string; ms?: number }[] = [
  { value: '15m', label: '15 min', ms: 15 * MINUTE },
  { value: '1h', label: '1 hour', ms: HOUR },
  { value: '24h', label: '24 hours', ms: DAY },
  { value: 'custom', label: 'Custom' },
]
const DEFAULT_RANGE: LogSearchRange = '1h'

export interface SearchForm {
  serviceIds: string[]
  range: LogSearchRange
  from?: string
  to?: string
  query: string
}

const isRange = (value: string | null): value is LogSearchRange => RANGES.some((r) => r.value === value)

/** Reads the form from the page URL. Unknown ranges fall back to 1 hour; service ids are de-duplicated and capped at 20. */
export function formFromParams(p: URLSearchParams): SearchForm {
  const range = p.get('range')
  const form: SearchForm = {
    serviceIds: [...new Set(p.getAll('service').filter(Boolean))].slice(0, MAX_SERVICES),
    range: isRange(range) ? range : DEFAULT_RANGE,
    query: p.get('q') ?? '',
  }
  const from = p.get('from')
  const to = p.get('to')
  if (form.range === 'custom') {
    if (from) form.from = from
    if (to) form.to = to
  }
  return form
}

/** Writes the form to URL parameters; empty values are left out. */
export function paramsFromForm(f: SearchForm): URLSearchParams {
  const p = new URLSearchParams()
  for (const id of f.serviceIds) p.append('service', id)
  p.set('range', f.range)
  if (f.range === 'custom') {
    if (f.from) p.set('from', f.from)
    if (f.to) p.set('to', f.to)
  }
  if (f.query) p.set('q', f.query)
  return p
}

/** The message to show under the form, or null when the search may run. Mirrors the server's checks. */
export function validateForm(f: SearchForm, now: number): string | null {
  if (f.serviceIds.length === 0) return 'Choose at least one service.'
  if (f.serviceIds.length > MAX_SERVICES) return `Choose at most ${MAX_SERVICES} services.`
  if (f.range !== 'custom') return null
  const from = f.from ? Date.parse(f.from) : NaN
  const to = f.to ? Date.parse(f.to) : NaN
  if (Number.isNaN(from) || Number.isNaN(to)) return 'Choose a start and end time.'
  if (from >= to) return 'The start must be before the end.'
  if (to - from > MAX_RANGE_MS) return 'The range can be at most 7 days.'
  if (to > now + FUTURE_SLACK_MS) return 'The end cannot be in the future.'
  return null
}

function bucketSize(from: number, to: number): number {
  const span = to - from
  const preset = RANGES.find((r) => r.ms !== undefined && Math.abs(r.ms - span) <= SECOND)
  if (preset?.value === '15m') return 30 * SECOND
  if (preset?.value === '1h') return MINUTE
  if (preset?.value === '24h') return 30 * MINUTE
  const fits = (size: number) => Math.floor(to / size) - Math.floor(from / size) + 1 <= MAX_BUCKETS
  return CUSTOM_BUCKET_SIZES.find(fits) ?? CUSTOM_BUCKET_SIZES[CUSTOM_BUCKET_SIZES.length - 1]
}

/** Counts rows per time bucket over [from, to] (epoch ms). Buckets are aligned to their size and empty ones are kept. */
export function histogramBuckets(rows: { t: string }[], from: number, to: number): { start: number; count: number }[] {
  const size = bucketSize(from, to)
  const first = Math.floor(from / size) * size
  const last = Math.floor(to / size) * size
  const buckets: { start: number; count: number }[] = []
  for (let start = first; start <= last; start += size) buckets.push({ start, count: 0 })
  for (const row of rows) {
    const index = Math.floor((Date.parse(row.t) - first) / size)
    if (index >= 0 && index < buckets.length) buckets[index].count++
  }
  return buckets
}

/** Rows matched per searched service, highest first; services with no rows are listed with 0. */
export function hitsByService(rows: { service_id: string }[], services: { id: string; name: string }[]): { id: string; name: string; count: number }[] {
  const counts = new Map<string, number>()
  for (const row of rows) counts.set(row.service_id, (counts.get(row.service_id) ?? 0) + 1)
  return services
    .map((s) => ({ id: s.id, name: s.name, count: counts.get(s.id) ?? 0 }))
    .sort((a, b) => b.count - a.count || a.name.localeCompare(b.name))
}

export function formatCost(bytes: number): string {
  const usd = (bytes / GB) * COST_PER_GB_USD
  return usd < 0.005 ? 'less than $0.01' : `about $${usd.toFixed(2)}`
}

export function formatBytes(bytes: number): string {
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  if (unit === 0) return `${Math.round(value)} B`
  return `${value >= 100 ? Math.round(value) : Math.round(value * 10) / 10} ${units[unit]}`
}

/** The Logs tab link for a result line: `before` is one second after the line so the line itself is included. */
export function logsTabHref(row: { service_id: string; t: string }): string {
  const before = new Date(Date.parse(row.t) + SECOND).toISOString()
  return `/services/${row.service_id}/logs?before=${encodeURIComponent(before)}`
}
