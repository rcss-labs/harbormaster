// Pure helpers for the Off-hours page: presets, grid maths and zone-aware time labels.
import type { AwakeGrid, OffHoursService } from '../../../api/offhours'

export const DAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'] as const

export const PRESETS = ['Weekdays 07–20', 'Weekdays 08–18', 'Always on'] as const
export type PresetName = (typeof PRESETS)[number]

const PRESET_SHAPE: Record<PresetName, { from: number; to: number; weekdaysOnly: boolean }> = {
  'Weekdays 07–20': { from: 7, to: 20, weekdaysOnly: true },
  'Weekdays 08–18': { from: 8, to: 18, weekdaysOnly: true },
  'Always on': { from: 0, to: 24, weekdaysOnly: false },
}

/** A new 7x24 grid (Monday first) for the preset; callers may mutate it. */
export function presetGrid(name: PresetName): AwakeGrid {
  const { from, to, weekdaysOnly } = PRESET_SHAPE[name]
  return DAYS.map((_, day) => Array.from({ length: 24 }, (_, hour) => (!weekdaysOnly || day < 5) && hour >= from && hour < to))
}

export function emptyGrid(): AwakeGrid {
  return DAYS.map(() => Array<boolean>(24).fill(false))
}

export function awakeHours(grid: AwakeGrid): number {
  return grid.reduce((total, day) => total + day.filter(Boolean).length, 0)
}

export interface WallClock {
  weekday: (typeof DAYS)[number]
  /** 0 is Monday. */
  day: number
  hour: number
  minute: number
  /** HH:MM */
  time: string
}

const formatters = new Map<string, Intl.DateTimeFormat>()
function formatterFor(timeZone: string): Intl.DateTimeFormat {
  let formatter = formatters.get(timeZone)
  if (!formatter) {
    formatter = new Intl.DateTimeFormat('en-US', { timeZone, weekday: 'short', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
    formatters.set(timeZone, formatter)
  }
  return formatter
}

/** The weekday and time of an instant on the clock of `timeZone`. */
export function wallClock(instant: string | Date, timeZone: string): WallClock {
  const parts = formatterFor(timeZone).formatToParts(typeof instant === 'string' ? new Date(instant) : instant)
  const part = (type: string) => parts.find((p) => p.type === type)?.value ?? ''
  const weekday = part('weekday') as WallClock['weekday']
  const hour = Number(part('hour'))
  const minute = Number(part('minute'))
  return {
    weekday,
    day: Math.max(0, DAYS.indexOf(weekday)),
    hour,
    minute,
    time: `${String(hour).padStart(2, '0')}:${String(minute).padStart(2, '0')}`,
  }
}

/** The grid cell that holds `instant` in `timeZone`. */
export function currentCell(instant: string | Date, timeZone: string): { day: number; hour: number } {
  const { day, hour } = wallClock(instant, timeZone)
  return { day, hour }
}

/** The state pill text. `now` is the server clock; weekday and time are read in the schedule's zone. */
export function stateLabel(service: Pick<OffHoursService, 'state' | 'until'>, now: string | Date, timeZone: string): string {
  const { until } = service
  switch (service.state) {
    case 'asleep': {
      if (!until) return 'Asleep'
      const at = wallClock(until, timeZone)
      return `Asleep until ${at.weekday} ${at.time}`
    }
    case 'woken': {
      if (!until) return 'Woken'
      // A wake lasts two hours, so the time alone is clear; the weekday only appears if the end is a day or more away.
      const at = wallClock(until, timeZone)
      const farAway = new Date(until).getTime() - new Date(now).getTime() >= 24 * 3_600_000
      return `Woken until ${farAway ? `${at.weekday} ` : ''}${at.time}`
    }
    case 'awake':
      return 'Awake'
    case 'blocked':
      return 'Blocked'
    case 'pending':
      return 'Pending'
    case 'not_managed':
      return 'Not managed'
    case 'conflict':
      return 'Also matches another schedule'
  }
}

/** The state pill colour. */
export function stateKind(state: OffHoursService['state']): 'ok' | 'info' | 'warn' | 'bad' | 'mute' {
  switch (state) {
    case 'awake':
      return 'ok'
    case 'asleep':
      return 'info'
    case 'woken':
      return 'warn'
    case 'blocked':
    case 'conflict':
      return 'bad'
    case 'pending':
    case 'not_managed':
      return 'mute'
  }
}

export const NOT_PRICED = 'Not priced: EC2 or unknown region'

export interface TagSelector {
  key: string
  value: string
}

/** Parses `key=value`; the value may be empty (`key=`), as the server accepts. Null without a key or an equals sign. */
export function parseTagSelector(text: string): TagSelector | null {
  const at = text.indexOf('=')
  if (at < 0) return null
  const key = text.slice(0, at).trim()
  const value = text.slice(at + 1).trim()
  return key ? { key, value } : null
}

export function browserTimezone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
}
