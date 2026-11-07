import { describe, expect, it } from 'vitest'
import type { TimelineEvent, TimelineLogErrorsDTO } from '../../../api/timeline'
import { groupMarkers, logEvents, smallestRangeContaining } from './model'

const FROM = 0
const TO = 1000

function marker(id: string, at: number, severity: TimelineEvent['severity'] = 'change'): TimelineEvent {
  return {
    id, lane: 'change', severity, title: id, actor: '', detail: '', op: '', task_id: '',
    at: new Date(at).toISOString(), evidence_at: null, diff: [], from_revision: null, to_revision: null,
  }
}

describe('groupMarkers', () => {
  it('groups markers closer than 2.5% and marks the group bad if any item is bad', () => {
    const groups = groupMarkers([marker('c', 300, 'bad'), marker('a', 100), marker('b', 120), marker('far', 800, 'info')], FROM, TO)
    expect(groups.map((group) => group.items.map((item) => item.id))).toEqual([['a', 'b'], ['c'], ['far']])
    expect(groups[0].pct).toBeCloseTo(10)
    expect(groups[0].severity).toBe('change')
    const mixed = groupMarkers([marker('a', 100), marker('b', 110, 'bad')], FROM, TO)
    expect(mixed).toHaveLength(1)
    expect(mixed[0].severity).toBe('bad')
  })

  it('keeps markers 2.5% apart separate', () => {
    expect(groupMarkers([marker('a', 100), marker('b', 125)], FROM, TO)).toHaveLength(2)
  })
})

const START = Date.parse('2026-11-07T11:00:00Z')

function bucket(index: number, fields: Partial<TimelineLogErrorsDTO['buckets'][number]> = {}) {
  return {
    start: new Date(START + index * 300_000).toISOString(), count: 0, capped: false, known: true, top_message: '', ...fields,
  }
}

function logErrors(buckets: TimelineLogErrorsDTO['buckets']): TimelineLogErrorsDTO {
  return { state: 'ok', message: '', bucket_seconds: 300, buckets, fetched_at: null }
}

describe('logEvents', () => {
  it('skips zero and unknown buckets and says at least when capped', () => {
    const data = logErrors([
      bucket(0),
      bucket(1, { count: 9, known: false }),
      bucket(2, { count: 214, top_message: 'OutOfMemoryError' }),
      bucket(3, { count: 120, capped: true }),
    ])
    const to = START + 4 * 300_000
    const events = logEvents(data, to)
    expect(events.map((event) => event.title)).toEqual(['214 error lines in 5 minutes', 'At least 120 error lines in 5 minutes'])
    expect(events[0]).toMatchObject({
      id: `log:${START + 2 * 300_000}`, lane: 'logs', severity: 'warn', actor: 'CloudWatch Logs',
      at: new Date(START + 3 * 300_000).toISOString(), detail: 'Most frequent: "OutOfMemoryError".',
    })
  })

  it('clamps the time of the open bucket to now', () => {
    const now = START + 330_000
    const [event] = logEvents(logErrors([bucket(1, { count: 3 })]), now)
    expect(event.at).toBe(new Date(now).toISOString())
  })

  it('gives a typical rate only with six or more buckets', () => {
    const counts = [0, 2, 4, 6, 8, 100]
    const six = logErrors(counts.map((count, index) => bucket(index, { count, top_message: 'boom' })))
    const to = START + 6 * 300_000
    const withRate = logEvents(six, to).at(-1)
    expect(withRate?.detail).toBe('Most frequent: "boom". Usually around 5 per 5 minutes.')
    const five = logErrors(counts.slice(0, 5).map((count, index) => bucket(index, { count, top_message: 'boom' })))
    expect(logEvents(five, to).at(-1)?.detail).toBe('Most frequent: "boom".')
  })
})

describe('smallestRangeContaining', () => {
  const NOW = Date.parse('2026-11-07T12:00:00Z')
  it('returns null beyond 7 days', () => {
    expect(smallestRangeContaining(NOW - 30 * 60_000, NOW)).toBe('1h')
    expect(smallestRangeContaining(NOW - 2 * 3_600_000, NOW)).toBe('6h')
    expect(smallestRangeContaining(NOW - 3 * 86_400_000, NOW)).toBe('7d')
    expect(smallestRangeContaining(NOW - 8 * 86_400_000, NOW)).toBeNull()
  })
})
