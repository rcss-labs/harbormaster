import { describe, expect, it } from 'vitest'
import type { ReviewSchedule } from '../../../api/agent'
import { describeSchedule, formatInZone, formatMinute, normalizeSchedule, parseTime } from './schedule'

const base: ReviewSchedule = { cadence: 'daily', at_minute: 480, weekday: null, month_day: null, timezone: 'Europe/Berlin' }

describe('schedule', () => {
  it('describes daily, weekly and monthly schedules', () => {
    expect(describeSchedule(base)).toBe('Daily at 08:00, Europe/Berlin')
    expect(describeSchedule({ ...base, cadence: 'weekly', weekday: 1 })).toBe('Weekly on Monday at 08:00, Europe/Berlin')
    expect(describeSchedule({ ...base, cadence: 'weekly', weekday: 7, at_minute: 1439, timezone: 'UTC' })).toBe('Weekly on Sunday at 23:59, UTC')
    expect(describeSchedule({ ...base, cadence: 'monthly', month_day: 1, at_minute: 5 })).toBe('Monthly on the 1st at 00:05, Europe/Berlin')
    expect(describeSchedule({ ...base, cadence: 'monthly', month_day: 22 })).toBe('Monthly on the 22nd at 08:00, Europe/Berlin')
    expect(describeSchedule({ ...base, cadence: 'monthly', month_day: 13 })).toBe('Monthly on the 13th at 08:00, Europe/Berlin')
  })

  it('names a cadence it does not know instead of guessing', () => {
    expect(describeSchedule({ ...base, cadence: 'hourly' as ReviewSchedule['cadence'] })).toBe('hourly at 08:00, Europe/Berlin')
  })

  it('reads and writes times of day', () => {
    expect(formatMinute(0)).toBe('00:00')
    expect(parseTime('08:30')).toBe(510)
    expect(parseTime('24:00')).toBeNull()
    expect(parseTime('')).toBeNull()
  })

  it('keeps the weekday only for weekly and the day only for monthly', () => {
    expect(normalizeSchedule({ ...base, cadence: 'daily', weekday: 3, month_day: 9 })).toMatchObject({ weekday: null, month_day: null })
    expect(normalizeSchedule({ ...base, cadence: 'weekly', weekday: 3, month_day: 9 })).toMatchObject({ weekday: 3, month_day: null })
    expect(normalizeSchedule({ ...base, cadence: 'monthly', weekday: 3, month_day: 9 })).toMatchObject({ weekday: null, month_day: 9 })
  })

  it('shows an instant in the review zone', () => {
    expect(formatInZone('2026-12-07T07:00:00Z', 'Europe/Berlin')).toContain('08:00')
    expect(formatInZone('2026-12-07T07:00:00Z', 'Not/AZone')).not.toBe('')
  })
})
