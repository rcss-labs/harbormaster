import { describe, expect, it } from 'vitest'
import { OFFHOURS_ASLEEP, OFFHOURS_BLOCKED, OFFHOURS_NOT_MANAGED, OFFHOURS_PRODUCTION, OFFHOURS_UNPRICED, OFFHOURS_WOKEN } from '../../../test/offhoursFixtures'
import { PRESETS, awakeHours, currentCell, presetGrid, stateLabel, wallClock } from './offhours'

const NOW = '2026-11-07T21:00:00Z'

describe('presets', () => {
  it('have 65, 50 and 168 awake hours', () => {
    expect(PRESETS.map((name) => awakeHours(presetGrid(name)))).toEqual([65, 50, 168])
    expect(PRESETS).toEqual(['Weekdays 07–20', 'Weekdays 08–18', 'Always on'])
  })

  it('hand out a fresh grid each time', () => {
    const first = presetGrid('Always on')
    first[0][0] = false
    expect(presetGrid('Always on')[0][0]).toBe(true)
  })
})

describe('stateLabel', () => {
  it('formats the wake time in the schedule zone, not the browser zone', () => {
    // 06:00Z on Monday is 07:00 in Berlin (CET) and 08:00 in Jerusalem (IST).
    expect(stateLabel(OFFHOURS_ASLEEP, NOW, 'Europe/Berlin')).toBe('Asleep until Mon 07:00')
    expect(stateLabel(OFFHOURS_ASLEEP, NOW, 'Asia/Jerusalem')).toBe('Asleep until Mon 08:00')
    // 23:00Z on Saturday is already Sunday 08:00 in Tokyo, so the weekday comes from the zone too.
    expect(stateLabel({ ...OFFHOURS_ASLEEP, until: '2026-11-07T23:00:00Z' }, NOW, 'Asia/Tokyo')).toBe('Asleep until Sun 08:00')
  })

  it('labels the other states', () => {
    expect(stateLabel(OFFHOURS_WOKEN, NOW, 'Europe/Berlin')).toBe('Woken until 00:00')
    expect(stateLabel(OFFHOURS_UNPRICED, NOW, 'Europe/Berlin')).toBe('Awake')
    expect(stateLabel(OFFHOURS_BLOCKED, NOW, 'Europe/Berlin')).toBe('Blocked')
    expect(stateLabel(OFFHOURS_PRODUCTION, NOW, 'Europe/Berlin')).toBe('Pending')
    expect(stateLabel(OFFHOURS_NOT_MANAGED, NOW, 'Europe/Berlin')).toBe('Not managed')
    expect(stateLabel({ ...OFFHOURS_ASLEEP, state: 'conflict' }, NOW, 'Europe/Berlin')).toBe('Also matches another schedule')
  })
})

describe('wall clock', () => {
  it('reads the weekday and time in the zone', () => {
    expect(wallClock(NOW, 'Europe/Berlin')).toEqual({ weekday: 'Sat', day: 5, hour: 22, minute: 0, time: '22:00' })
    expect(currentCell(NOW, 'Asia/Jerusalem')).toEqual({ day: 5, hour: 23 })
    expect(currentCell('2026-11-08T22:30:00Z', 'Asia/Jerusalem')).toEqual({ day: 0, hour: 0 })
  })
})
