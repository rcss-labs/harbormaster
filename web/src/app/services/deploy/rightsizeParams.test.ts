import { describe, expect, it } from 'vitest'
import { parseRightsizeParams, rightsizeHref, rightsizeReason } from './rightsizeParams'

const paramsOf = (href: string) => new URL(href, 'http://hm.test').searchParams

describe('rightsize URL parameters', () => {
  it('round trips a size and headroom through the Deploy link', () => {
    const href = rightsizeHref('svc-1', { cpuUnits: 256, memoryMib: 1024 }, 30)
    expect(href.startsWith('/services/svc-1/deploy?')).toBe(true)
    expect(parseRightsizeParams(paramsOf(href))).toEqual({ cpu: '256', memory: '1024', headroomPct: 30 })
  })

  it.each([
    ['an invalid cpu and memory pair', 'task_cpu=512&task_memory=512&headroom=30'],
    ['headroom below 10', 'task_cpu=256&task_memory=1024&headroom=5'],
    ['headroom above 100', 'task_cpu=256&task_memory=1024&headroom=105'],
    ['non-numeric headroom', 'task_cpu=256&task_memory=1024&headroom=abc'],
    ['fractional headroom', 'task_cpu=256&task_memory=1024&headroom=30.5'],
    ['an inherited object key as cpu', 'task_cpu=constructor&task_memory=512&headroom=30'],
    ['__proto__ as cpu', 'task_cpu=__proto__&task_memory=512&headroom=30'],
    ['a missing memory', 'task_cpu=256&headroom=30'],
  ])('rejects %s', (_name, query) => {
    expect(parseRightsizeParams(new URLSearchParams(query))).toBeNull()
  })

  it('words the reason with the headroom', () => {
    expect(rightsizeReason(30)).toBe('Rightsizing at 30% headroom')
  })
})
