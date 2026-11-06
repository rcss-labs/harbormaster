import { describe, expect, it } from 'vitest'
import { SERVICES_LIST } from '../../../test/fixtures'
import { applyQuery, applyStatus, applyView, needsAttention, sortServices, statusCounts, taskTone } from './filterServices'

const items = SERVICES_LIST.items
const names = (list: { name: string }[]) => list.map((s) => s.name)
const byName = (a: string, b: string) => a.localeCompare(b)

describe('applyView', () => {
  it('all keeps everything', () => {
    expect(applyView(items, 'all', new Set())).toHaveLength(15)
  })
  it('problems keeps every service that is not steady', () => {
    const shown = applyView(items, 'problems', new Set())
    expect(shown.length).toBeGreaterThan(0)
    expect(shown.every((s) => s.status !== 'steady')).toBe(true)
    expect(names(shown)).toContain('image-resizer')
  })
  it('problems follows status or an open alert, and leaves out a service scaled to zero on purpose', () => {
    const base = items.find((s) => s.status === 'steady')!
    const scaled = {
      ...base,
      status: 'scaled_to_zero' as const,
      attention: '',
    }
    expect(needsAttention(scaled)).toBe(false)
    expect(needsAttention({ ...base, attention: '' })).toBe(false)
    expect(needsAttention({ ...base, attention: 'Restart loop' })).toBe(true)
    expect(needsAttention({ ...base, status: 'degraded', attention: '' })).toBe(true)
    expect(needsAttention({ ...base, status: 'deploying', attention: '' })).toBe(true)
  })
  it('pins keeps only pinned ids', () => {
    const pinned = new Set([items[0].id, items[3].id])
    expect(applyView(items, 'pins', pinned).map((s) => s.id)).toEqual([items[0].id, items[3].id])
  })
  it('a tag view keeps services of that tag id', () => {
    const prod = applyView(items, 'tag-3', new Set())
    expect(prod.length).toBeGreaterThan(0)
    expect(prod.every((s) => s.tag.id === 3)).toBe(true)
  })
})

describe('applyStatus', () => {
  it('any keeps everything; a kind keeps services whose status has that colour', () => {
    expect(applyStatus(items, 'any')).toHaveLength(15)
    expect(names(applyStatus(items, 'bad'))).toEqual(['image-resizer'])
    expect(applyStatus(items, 'ok').every((s) => s.status === 'steady')).toBe(true)
  })
  it('counts add up to the list', () => {
    const counts = statusCounts(items)
    expect(counts.any).toBe(15)
    expect(counts.bad + counts.warn + counts.info + counts.ok + counts.mute).toBe(15)
  })
})

describe('applyQuery', () => {
  it('matches name, cluster, account, region and image tag, case-insensitively', () => {
    expect(names(applyQuery(items, 'PAYMENTS'))).toContain('payments-api')
    expect(applyQuery(items, 'core-services').every((s) => s.cluster.name === 'core-services')).toBe(true)
    expect(applyQuery(items, 'eu-west-1').every((s) => s.region === 'eu-west-1')).toBe(true)
    expect(names(applyQuery(items, '1.18.2'))).toContain('payments-api')
  })
  it('an empty or blank query keeps everything; no match gives nothing', () => {
    expect(applyQuery(items, '  ')).toHaveLength(15)
    expect(applyQuery(items, 'zzz-nothing')).toHaveLength(0)
  })
})

describe('sortServices', () => {
  it('status descending puts the worst first and breaks ties by name', () => {
    const sorted = sortServices(items, 'status', 'desc')
    expect(sorted[0].name).toBe('image-resizer')
    const steady = sorted.filter((s) => s.status === 'steady').map((s) => s.name)
    expect(steady).toEqual([...steady].sort(byName))
  })
  it('name ascending and descending', () => {
    const expected = names(items).sort(byName)
    expect(names(sortServices(items, 'name', 'asc'))).toEqual(expected)
    expect(names(sortServices(items, 'name', 'desc'))).toEqual([...expected].reverse())
  })
  it('tasks sorts by the running share of desired', () => {
    const sorted = sortServices(items, 'tasks', 'asc')
    expect(sorted[0].running).toBeLessThan(sorted[0].desired)
  })
  it('services without a last change stay last in both directions', () => {
    const list = [
      { ...items[0], name: 'a', last_change_at: null },
      { ...items[1], name: 'b', last_change_at: '2026-11-01T00:00:00Z' },
      { ...items[2], name: 'c', last_change_at: '2026-11-02T00:00:00Z' },
    ]
    expect(names(sortServices(list, 'last', 'asc'))).toEqual(['b', 'c', 'a'])
    expect(names(sortServices(list, 'last', 'desc'))).toEqual(['c', 'b', 'a'])
  })
  it('does not mutate its input', () => {
    const copy = [...items]
    sortServices(items, 'name', 'desc')
    expect(items).toEqual(copy)
  })
})

describe('taskTone', () => {
  it('is bad when fewer tasks run than are desired', () => {
    expect(taskTone({ running: 1, desired: 2 })).toBe('bad')
    expect(taskTone({ running: 2, desired: 2 })).toBeUndefined()
    expect(taskTone({ running: 0, desired: 0 })).toBeUndefined()
  })
})
