/** Compact number: 12345 → "12k", 12.34 → "12", 1.27 → "1.3" (mockup fmtNum). */
export const fmtNum = (value: number): string => {
  if (value >= 1000) return (value / 1000).toFixed(value >= 10000 ? 0 : 1) + 'k'
  if (value >= 10 || value === 0) return String(Math.round(value))
  return String(+value.toFixed(1))
}

/** Rounds up to 1, 2 or 5 significant steps of the value's order of magnitude (mockup niceMax). */
export const niceMax = (value: number): number => {
  const magnitude = Math.pow(10, Math.floor(Math.log10(Math.max(value, 1))))
  return Math.ceil(value / magnitude) * magnitude
}

/** Percentage of total, rounded; 0 when total is 0. */
export const pct = (value: number, total: number): number => (total ? Math.round((value / total) * 100) : 0)

/** Short date for a timeline tick, e.g. "Oct 31". */
export const dayLabel = (ts: number): string => new Date(ts).toLocaleDateString([], { month: 'short', day: 'numeric' })

export const XL24 = ['24 h ago', '18 h', '12 h', '6 h', 'now']
export const XL72 = ['72 h ago', '48 h', '24 h', 'now']

/** Short date for a bucket day from the API, which is a bare date or an RFC 3339 midnight ("2026-10-31T00:00:00Z"). */
export const bucketDayLabel = (day: string): string => dayLabel(Date.parse(`${day.slice(0, 10)}T00:00:00`))

/** One character of the 12px mono axis face, rounded up. */
const AXIS_CHAR_PX = 7.3

/** Width of the value axis: as wide as its longest label, so a chart of small numbers starts at the panel's edge. */
export function axisWidth(labels: string[]): string {
  return `${Math.ceil(Math.max(1, ...labels.map((label) => label.length)) * AXIS_CHAR_PX)}px`
}

const DAY_MS = 86_400_000

/** Milliseconds between two neighbouring buckets; 0 for a single bucket. */
export const bucketSpacing = (timestamps: string[]): number => (timestamps.length > 1 ? Date.parse(timestamps[1]) - Date.parse(timestamps[0]) : 0)

/**
 * Calendar date of a bucket for a tooltip: "Sat, Nov 21" for day-sized buckets (the API's own day, whatever the viewer's
 * time zone), with the local time for shorter ones. The year is added once it is not the current one.
 */
export function bucketDateTime(timestamp: string, spacing: number, now: number): string {
  // A lone bucket has no spacing; the API stamps day buckets at midnight UTC or as a bare date.
  const dayBucket = spacing >= DAY_MS || (spacing === 0 && /^\d{4}-\d{2}-\d{2}(T00:00:00(\.0+)?Z)?$/.test(timestamp))
  const at = dayBucket ? new Date(`${timestamp.slice(0, 10)}T00:00:00`) : new Date(timestamp)
  const otherYear = at.getFullYear() !== new Date(now).getFullYear()
  const date = at.toLocaleDateString([], { weekday: 'short', month: 'short', day: 'numeric', ...(otherYear ? { year: 'numeric' as const } : {}) })
  return dayBucket ? date : `${date}, ${at.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`
}
