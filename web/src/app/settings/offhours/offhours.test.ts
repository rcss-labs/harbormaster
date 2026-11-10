import { describe, expect, it } from 'vitest'
import { awsSelector, harbormasterSelector, OFFHOURS_ASLEEP, OFFHOURS_BLOCKED, OFFHOURS_NOT_MANAGED, OFFHOURS_PRODUCTION, OFFHOURS_UNPRICED, OFFHOURS_WOKEN } from '../../../test/offhoursFixtures'
import { PRESETS, awakeHours, currentCell, parseAddableAwsTag, parseTagSelector, presetGrid, selectorFromKey, selectorKey, stateLabel, tagSummary, wallClock } from './offhours'

describe('parseTagSelector', () => {
  it('accepts key=value and key= with an empty value, as the server does', () => {
    expect(parseTagSelector('env=staging')).toEqual({ key: 'env', value: 'staging' })
    expect(parseTagSelector(' env = ')).toEqual({ key: 'env', value: '' })
    expect(parseTagSelector('env=')).toEqual({ key: 'env', value: '' })
  })

  it('rejects text without a key or without an equals sign', () => {
    expect(parseTagSelector('env')).toBeNull()
    expect(parseTagSelector('=prod')).toBeNull()
    expect(parseTagSelector('')).toBeNull()
  })
})

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

describe('tag choices', () => {
  it('round-trip through their option keys', () => {
    for (const input of [
      { kind: 'harbormaster', tag_id: 7 } as const,
      { kind: 'aws', key: 'env', value: 'a=b' } as const,
      { kind: 'aws', key: 'env', value: '' } as const,
    ]) {
      expect(selectorFromKey(selectorKey(input))).toEqual(input)
    }
    expect(selectorFromKey('nonsense')).toBeNull()
  })

  it('refuses aws: keys when adding a typed tag', () => {
    expect(parseAddableAwsTag('team=web')).toEqual({ key: 'team', value: 'web' })
    expect(parseAddableAwsTag('aws:cloudformation:stack-name=x')).toBeNull()
    expect(parseAddableAwsTag('team')).toBeNull()
  })
})

describe('tagSummary', () => {
  const [a, b, c, d] = [harbormasterSelector(1, 'QA', 'blue'), awsSelector('env', 'staging'), awsSelector('a', 'b'), awsSelector('c', 'd')]
  it('lists up to three tags then counts the rest', () => {
    expect(tagSummary([b, a], 0)).toBe('env=staging, QA')
    expect(tagSummary([a, b, c], 0)).toBe('QA, env=staging, a=b')
    expect(tagSummary([a, b, c, d], 0)).toBe('QA, env=staging, a=b, +1 more')
  })
  it('adds the excluded count only when there are exclusions', () => {
    expect(tagSummary([b], 2)).toBe('env=staging · 2 excluded')
  })
})
