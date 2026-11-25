import type { ReviewSchedule } from '../../../api/agent'

export const WEEKDAYS = ['Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday', 'Sunday']

const pad2 = (n: number) => String(n).padStart(2, '0')

/** Local minutes after midnight as HH:MM. */
export function formatMinute(minute: number): string {
  return `${pad2(Math.floor(minute / 60))}:${pad2(minute % 60)}`
}

/** HH:MM from a time field to minutes after midnight; null for anything else. */
export function parseTime(text: string): number | null {
  const match = /^(\d{2}):(\d{2})$/.exec(text)
  if (!match) return null
  const hours = Number(match[1])
  const minutes = Number(match[2])
  return hours < 24 && minutes < 60 ? hours * 60 + minutes : null
}

function ordinal(n: number): string {
  const tens = n % 100
  if (tens >= 11 && tens <= 13) return `${n}th`
  return `${n}${({ 1: 'st', 2: 'nd', 3: 'rd' } as Record<number, string>)[n % 10] ?? 'th'}`
}

/** "Weekly on Monday at 08:00, Europe/Berlin". A cadence this version does not know is shown as it came. */
export function describeSchedule(schedule: ReviewSchedule): string {
  const at = `at ${formatMinute(schedule.at_minute)}, ${schedule.timezone}`
  switch (schedule.cadence) {
    case 'daily':
      return `Daily ${at}`
    case 'weekly':
      return `Weekly on ${WEEKDAYS[(schedule.weekday ?? 1) - 1] ?? 'an unknown day'} ${at}`
    case 'monthly':
      return `Monthly on the ${ordinal(schedule.month_day ?? 1)} ${at}`
    default:
      return `${String(schedule.cadence)} ${at}`
  }
}

/** The weekday belongs to weekly schedules only and the day of the month to monthly ones; the other is cleared. */
export function normalizeSchedule(schedule: ReviewSchedule): ReviewSchedule {
  return {
    ...schedule,
    weekday: schedule.cadence === 'weekly' ? schedule.weekday : null,
    month_day: schedule.cadence === 'monthly' ? schedule.month_day : null,
  }
}

/** An instant as date and time in the review's own zone; the browser's zone if the name is not one it knows. */
export function formatInZone(iso: string, timezone: string): string {
  const at = new Date(iso)
  try {
    return at.toLocaleString(undefined, { timeZone: timezone, dateStyle: 'medium', timeStyle: 'short', hour12: false }) + ` ${timezone}`
  } catch {
    return at.toLocaleString()
  }
}

let zoneCache: string[] | null = null

/** The zones the browser knows, with UTC and the one in use always present. */
export function timeZoneOptions(current: string): string[] {
  if (!zoneCache) {
    const supported = typeof Intl.supportedValuesOf === 'function' ? Intl.supportedValuesOf('timeZone') : []
    zoneCache = supported.includes('UTC') ? supported : ['UTC', ...supported]
  }
  return zoneCache.includes(current) || current === '' ? zoneCache : [current, ...zoneCache]
}
