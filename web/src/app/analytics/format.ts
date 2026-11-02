import type { AnalyticsSeries, AnalyticsSource } from '../../api/analytics'
import type { TagColor } from '../../api/types'
import { dayLabel } from '../../charts/format'

/** Series colours; orange and pink come from the shared chart palette. */
export const COLOR = {
  ok: 'var(--success)',
  warn: 'var(--warning)',
  bad: 'var(--danger)',
  info: 'var(--info)',
  accent: 'var(--accent)',
  orange: 'var(--series-2)',
  pink: 'var(--series-5)',
  gray: 'var(--muted)',
  gray2: 'var(--dim)',
} as const

/** Colours for series whose keys are not known ahead (zones, failure reasons). */
export const CYCLE = [COLOR.accent, COLOR.warn, COLOR.ok, COLOR.info, COLOR.orange, COLOR.pink, COLOR.bad, COLOR.gray]

export const TAG_COLOR: Record<TagColor, string> = {
  red: COLOR.bad,
  amber: COLOR.warn,
  green: COLOR.ok,
  blue: COLOR.accent,
  violet: COLOR.info,
  neutral: COLOR.gray,
}

/** The instant `days` days before `iso`; the start of the rolling windows of the daily-rollup panels. */
export const daysBefore = (iso: string, days: number) => new Date(Date.parse(iso) - days * 86_400_000).toISOString()

export const whole = (value: number) => Math.round(value).toLocaleString('en-US')

/** One decimal, dropped when whole: 14.5, 36. */
export const oneDecimal = (value: number) => String(+value.toFixed(1))

/** 0..1 as a percentage with `digits` decimals. */
export const percent = (fraction: number, digits = 1) => `${(fraction * 100).toFixed(digits)}%`

/** Seconds as "45s", "5m 12s", "18m" or "1h 5m". */
export function durationText(seconds: number): string {
  const total = Math.round(seconds)
  if (total < 60) return `${total}s`
  if (total < 3600) {
    const rest = total % 60
    return `${Math.floor(total / 60)}m${rest ? ` ${rest}s` : ''}`
  }
  const minutes = Math.round((total % 3600) / 60)
  return `${Math.floor(total / 3600)}h${minutes ? ` ${minutes}m` : ''}`
}

/** Minutes as "12m", "1h 35m" or "2d 3h". */
export function minutesText(minutes: number): string {
  const total = Math.round(minutes)
  if (total < 60) return `${total}m`
  if (total < 48 * 60) {
    const rest = total % 60
    return `${Math.floor(total / 60)}h${rest ? ` ${rest}m` : ''}`
  }
  const hours = Math.round((total % 1440) / 60)
  return `${Math.floor(total / 1440)}d${hours ? ` ${hours}h` : ''}`
}

/** GiB as GiB below 1 TiB, otherwise TiB. */
export const memoryText = (gib: number) => (gib >= 1024 ? `${(gib / 1024).toFixed(2)} TiB` : `${oneDecimal(gib)} GiB`)

const WINDOW_TEXT: Record<string, string> = { '24h': '24 hours', '7d': '7 days', '30d': '30 days', '90d': '90 days' }
export const windowText = (window: string) => WINDOW_TEXT[window] ?? window

/** Length of a window such as "7d" or "24h" in days. */
export function windowDays(window: string): number {
  const amount = Number.parseInt(window, 10)
  if (Number.isNaN(amount) || amount <= 0) return 1
  return window.endsWith('h') ? amount / 24 : amount
}

export const sumOf =(values: (number | null)[]) => values.reduce<number>((total, value) => total + (value ?? 0), 0)

export function averageOf(values: (number | null)[]): number | null {
  const present = values.filter((value): value is number => value !== null)
  return present.length === 0 ? null : sumOf(present) / present.length
}

/** Start, middle and end labels: hours back for spans up to two days, dates otherwise. */
export function xLabelsFor(timestamps: string[]): [string, string, string] {
  if (timestamps.length < 2) return ['', '', 'now']
  const first = Date.parse(timestamps[0])
  const last = Date.parse(timestamps[timestamps.length - 1])
  const middle = Date.parse(timestamps[Math.floor((timestamps.length - 1) / 2)])
  if (last - first <= 2 * 86_400_000) {
    const hoursBack = (time: number) => Math.round((last - time) / 3_600_000)
    return [`${hoursBack(first)} h ago`, `${hoursBack(middle)} h`, 'now']
  }
  return [dayLabel(first), dayLabel(middle), 'now']
}

/** Colour of a line from its API key, falling back to the cycle by position. */
export function lineColor(known: Record<string, string>, key: string, index: number): string {
  return known[key] ?? CYCLE[index % CYCLE.length]
}

export interface TrendLine {
  key: string
  label: string
  color: string
  values: (number | null)[]
  expected?: boolean
  dashed?: boolean
}

/** Series lines with colours and (optionally) relabelled names, ready for TimeSeriesChart. */
export function linesOf(
  series: AnalyticsSeries,
  known: Record<string, string>,
  label: (line: AnalyticsSeries['series'][number]) => string = (line) => line.label,
): TrendLine[] {
  return series.series.map((line, index) => ({
    key: line.key,
    label: label(line),
    color: line.expected ? COLOR.gray : lineColor(known, line.key, index),
    values: line.values,
    expected: line.expected,
  }))
}

/** The source row for a table, shaped for the panel footer; the label overrides the table name when one panel reads several. */
export function sourceFor(sources: AnalyticsSource[], table: string, label?: string): { table: string; kept: string } | undefined {
  const found = sources.find((source) => source.table === table)
  return found ? { table: label ?? table, kept: found.kept } : undefined
}
