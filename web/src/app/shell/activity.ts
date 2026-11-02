import type { AlertDTO, EventDTO } from '../../api/types'

export type ActivityFilter = 'problems' | 'changes' | 'all'
export type ActivitySeverity = 'danger' | 'warning' | 'info' | 'ok'

export interface ActivityItem {
  /** Alerts are problems the poller currently sees; events are ECS or Harbormaster history. */
  kind: 'problem' | 'event'
  /** Epoch ms. */
  at: number
  subject: string
  text: string
  actor: string
  severity: ActivitySeverity
  /** True for events made through Harbormaster itself (restart, scale, ...). */
  change: boolean
  href: string
}

export interface ActivityRow extends ActivityItem {
  /** How many identical items this row stands for. */
  count: number
}

/** The rail is a glance, not a log: it never shows more rows than this. */
export const ACTIVITY_LIMIT = 40

function fromAlert(alert: AlertDTO): ActivityItem {
  return {
    kind: 'problem',
    at: Date.parse(alert.at),
    subject: alert.service || alert.cluster || alert.title,
    text: alert.service ? alert.text || alert.title : alert.title,
    actor: 'Harbormaster',
    severity: alert.severity,
    change: false,
    href: alert.service_id ? `/services/${alert.service_id}/${alert.tab || 'overview'}` : alert.cluster_id ? `/clusters/${alert.cluster_id}` : '/',
  }
}

function fromEvent(event: EventDTO): ActivityItem {
  const fromEcs = event.source === 'ecs'
  const href = event.service.id ? `/services/${event.service.id}/overview` : event.cluster.id ? `/clusters/${event.cluster.id}` : '/'
  return {
    kind: 'event',
    at: Date.parse(event.ts),
    subject: event.service.name || event.cluster.name,
    text: event.text,
    actor: fromEcs ? `ECS event · ${event.cluster.name}` : event.actor,
    severity: fromEcs ? 'info' : 'ok',
    change: !fromEcs,
    href,
  }
}

/** Alerts and events in one list, newest first. */
export function buildActivity(alerts: AlertDTO[], events: EventDTO[]): ActivityItem[] {
  return [...alerts.map(fromAlert), ...events.map(fromEvent)].sort((a, b) => b.at - a.at)
}

const isProblem = (item: ActivityItem) => item.kind === 'problem' && (item.severity === 'danger' || item.severity === 'warning')

export function filterActivity<T extends ActivityItem>(items: T[], filter: ActivityFilter): T[] {
  if (filter === 'problems') return items.filter(isProblem)
  if (filter === 'changes') return items.filter((item) => item.change)
  return items
}

/** Folds items with the same subject, text and source into one row (kept at the newest one's position), then bounds the list. */
export function collapseRepeats(items: ActivityItem[]): ActivityRow[] {
  const rows = new Map<string, ActivityRow>()
  for (const item of items) {
    const key = `${item.kind}\u0000${item.subject}\u0000${item.text}`
    const existing = rows.get(key)
    if (existing) existing.count += 1
    else rows.set(key, { ...item, count: 1 })
  }
  return [...rows.values()].slice(0, ACTIVITY_LIMIT)
}
