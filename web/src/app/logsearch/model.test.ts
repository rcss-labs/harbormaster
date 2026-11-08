import { describe, expect, it } from 'vitest'
import {
  formatBytes,
  formatCost,
  formFromParams,
  histogramBuckets,
  hitsByService,
  logsTabHref,
  paramsFromForm,
  validateForm,
  type SearchForm,
} from './model'

const MINUTE = 60_000
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR
const NOW = Date.parse('2026-11-08T12:00:00Z')

describe('log search model', () => {
  it('round-trips the form through the URL', () => {
    const form: SearchForm = {
      serviceIds: ['a', 'b'],
      range: 'custom',
      from: '2026-11-07T10:00:00.000Z',
      to: '2026-11-07T11:00:00.000Z',
      query: 'level=error',
    }
    expect(formFromParams(paramsFromForm(form))).toEqual(form)
    expect(paramsFromForm({ serviceIds: [], range: '1h', query: '' }).has('q')).toBe(false)
    expect(paramsFromForm({ serviceIds: [], range: '1h', query: '' }).has('from')).toBe(false)
  })

  it('falls back to 1 hour for an unknown range and keeps 20 services', () => {
    const ids = Array.from({ length: 25 }, (_, i) => `svc-${i}`)
    const params = new URLSearchParams([['range', '3w'], ...ids.map((id) => ['service', id]), ['service', 'svc-0']])
    const form = formFromParams(params)
    expect(form.range).toBe('1h')
    expect(form.serviceIds).toEqual(ids.slice(0, 20))
    expect(formFromParams(new URLSearchParams()).range).toBe('1h')
  })

  it('rejects a custom range over 7 days and a future end', () => {
    const base: SearchForm = { serviceIds: ['a'], range: 'custom', query: '' }
    const iso = (ms: number) => new Date(ms).toISOString()
    expect(validateForm({ ...base, from: iso(NOW - 8 * DAY), to: iso(NOW) }, NOW)).toMatch(/7 days/)
    expect(validateForm({ ...base, from: iso(NOW - HOUR), to: iso(NOW + HOUR) }, NOW)).toMatch(/future/)
    expect(validateForm({ ...base, from: iso(NOW - HOUR), to: iso(NOW - 2 * HOUR) }, NOW)).toMatch(/before/)
    expect(validateForm({ ...base }, NOW)).toMatch(/start and end/)
    expect(validateForm({ ...base, from: iso(NOW - 7 * DAY), to: iso(NOW) }, NOW)).toBeNull()
    expect(validateForm({ ...base, serviceIds: [], range: '1h' }, NOW)).toBe('Choose at least one service.')
    expect(validateForm({ ...base, serviceIds: Array.from({ length: 21 }, (_, i) => `s${i}`), range: '1h' }, NOW)).toBe('Choose at most 20 services.')
    expect(validateForm({ ...base, range: '1h' }, NOW)).toBeNull()
  })

  it('buckets rows by range size and keeps empty buckets', () => {
    const to = NOW
    const from = NOW - HOUR
    const rows = [{ t: new Date(from + 10_000).toISOString() }, { t: new Date(from + 20_000).toISOString() }, { t: new Date(from + 5 * MINUTE + 1).toISOString() }]
    const buckets = histogramBuckets(rows, from, to)
    // 1 hour range: 1 minute buckets aligned to the minute, covering [from, to].
    expect(buckets[0].start % MINUTE).toBe(0)
    expect(buckets.length).toBeGreaterThanOrEqual(60)
    expect(buckets.length).toBeLessThanOrEqual(61)
    expect(buckets.reduce((sum, b) => sum + b.count, 0)).toBe(3)
    expect(buckets.filter((b) => b.count === 0).length).toBeGreaterThan(50)
    expect(buckets[0].count).toBe(2)
    // 15 minutes: 30 s buckets.
    expect(histogramBuckets([], NOW - 15 * MINUTE, NOW)[1].start - histogramBuckets([], NOW - 15 * MINUTE, NOW)[0].start).toBe(30_000)
    // 24 hours: 30 min buckets.
    const day = histogramBuckets([], NOW - DAY, NOW)
    expect(day[1].start - day[0].start).toBe(30 * MINUTE)
  })

  it('chooses at most 60 buckets for a custom range', () => {
    for (const span of [10 * MINUTE, 90 * MINUTE, 3 * DAY, 7 * DAY, 7 * DAY - 1]) {
      const from = NOW - span
      const buckets = histogramBuckets([], from, NOW)
      expect(buckets.length).toBeLessThanOrEqual(60)
      expect(buckets[0].start).toBeLessThanOrEqual(from)
      expect(buckets[buckets.length - 1].start).toBeLessThanOrEqual(NOW)
    }
    // 3 days at 1 h would be 73 buckets, so 3 h.
    const three = histogramBuckets([], NOW - 3 * DAY, NOW)
    expect(three[1].start - three[0].start).toBe(3 * HOUR)
    // 90 minutes -> 30 s gives 180, 1 min 90, 5 min 18 -> picks 5 min.
    const ninety = histogramBuckets([], NOW - 90 * MINUTE, NOW)
    expect(ninety[1].start - ninety[0].start).toBe(5 * MINUTE)
  })

  it('counts hits by service, highest first', () => {
    const rows = [{ service_id: 'b' }, { service_id: 'a' }, { service_id: 'b' }, { service_id: 'c' }, { service_id: 'b' }, { service_id: 'a' }]
    const services = [
      { id: 'a', name: 'alpha' },
      { id: 'b', name: 'beta' },
      { id: 'd', name: 'delta' },
    ]
    expect(hitsByService(rows, services)).toEqual([
      { id: 'b', name: 'beta', count: 3 },
      { id: 'a', name: 'alpha', count: 2 },
      { id: 'd', name: 'delta', count: 0 },
    ])
  })

  it('formats cost about and below one cent', () => {
    const GB = 2 ** 30
    expect(formatCost(4 * GB)).toBe('about $0.02')
    expect(formatCost(GB / 2)).toBe('less than $0.01')
    expect(formatCost(0)).toBe('less than $0.01')
    expect(formatCost(2 * GB)).toBe('about $0.01')
    expect(formatBytes(1.4 * GB)).toBe('1.4 GB')
    expect(formatBytes(820 * 2 ** 20)).toBe('820 MB')
    expect(formatBytes(512)).toBe('512 B')
  })

  it('links a line to the Logs tab one second after its time', () => {
    const href = logsTabHref({ service_id: 'svc-1', t: '2026-11-08T10:00:00.250Z' })
    expect(href).toBe(`/services/svc-1/logs?before=${encodeURIComponent('2026-11-08T10:00:01.250Z')}`)
  })
})
