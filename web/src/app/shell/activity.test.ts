import { describe, expect, it } from 'vitest'
import type { AlertDTO, EventDTO } from '../../api/types'
import { ACTIVITY_LIMIT, buildActivity, collapseRepeats, filterActivity } from './activity'

const iso = (minutesAgo: number) => new Date(Date.UTC(2026, 5, 1, 12, 0) - minutesAgo * 60_000).toISOString()

const event = (minutesAgo: number, overrides: Partial<EventDTO> = {}): EventDTO => ({
  ts: iso(minutesAgo),
  source: 'ecs',
  actor: 'ECS',
  service: { id: 'svc-1', name: 'payments-api', status: 'steady' },
  cluster: { id: 'cl-1', name: 'core-services' },
  text: 'has reached a steady state.',
  ...overrides,
})

const alert = (minutesAgo: number, overrides: Partial<AlertDTO> = {}): AlertDTO => ({
  kind: 'service_failing',
  severity: 'danger',
  title: 'image-resizer: 0 of 2 tasks running',
  text: 'No task is running.',
  at: iso(minutesAgo),
  service_id: 'svc-9',
  service: 'image-resizer',
  cluster: 'background-jobs',
  cluster_id: 'cl-9',
  tab: 'tasks',
  cause: 'No task is running.',
  next_step: { label: 'Open tasks', href: '/services/svc-9/tasks' },
  just_before: null,
  ...overrides,
})

describe('buildActivity', () => {
  it('merges alerts and events, newest first, and links to the service tab', () => {
    const rows = buildActivity([alert(5)], [event(1), event(10)])
    expect(rows.map((r) => r.kind)).toEqual(['event', 'problem', 'event'])
    expect(rows[1].href).toBe('/services/svc-9/tasks')
    expect(rows[1].subject).toBe('image-resizer')
  })
})

describe('filterActivity', () => {
  const rows = buildActivity(
    [alert(5), alert(6, { severity: 'info' })],
    [event(1), event(2, { source: 'harbormaster', actor: 'sam', text: 'Restarted service.' })],
  )
  it('Problems keeps warning and danger alerts only', () => {
    expect(filterActivity(rows, 'problems')).toHaveLength(1)
  })
  it('Changes keeps events made through Harbormaster', () => {
    const changes = filterActivity(rows, 'changes')
    expect(changes).toHaveLength(1)
    expect(changes[0].actor).toBe('sam')
  })
  it('All keeps everything', () => {
    expect(filterActivity(rows, 'all')).toHaveLength(4)
  })
})

describe('collapseRepeats', () => {
  it('folds identical subject and text into one row with the count, keeping the newest time', () => {
    const rows = collapseRepeats(buildActivity([], [event(1), event(4), event(9), event(3, { text: 'other' })]))
    expect(rows).toHaveLength(2)
    const folded = rows.find((r) => r.text === 'has reached a steady state.')!
    expect(folded.count).toBe(3)
    expect(folded.at).toBe(Date.parse(iso(1)))
  })

  it('keeps events of different services apart', () => {
    const other = event(2, { service: { id: 'svc-2', name: 'catalog-api', status: 'steady' } })
    expect(collapseRepeats(buildActivity([], [event(1), other]))).toHaveLength(2)
  })

  it('bounds the list', () => {
    const many = Array.from({ length: ACTIVITY_LIMIT + 20 }, (_, i) => event(i, { text: `event ${i}` }))
    expect(collapseRepeats(buildActivity([], many))).toHaveLength(ACTIVITY_LIMIT)
  })
})
