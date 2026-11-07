import type { ServiceListItemDTO } from '../../../api/types'
import type { PillKind } from '../../../components/Pill'
import { statusKind } from '../../../components/status'
import { serviceSeverity } from '../../clusters/serviceSeverity'

/** 'all', 'problems', 'pins', or `tag-<id>` for one tag. */
export type ServiceView = string
export const VIEW_ALL = 'all'
export const VIEW_PROBLEMS = 'problems'
export const VIEW_PINS = 'pins'
export const tagView = (tagId: number): ServiceView => `tag-${tagId}`

/** Status segment: the colour group of the service status. */
export type ServiceStatusFilter = 'any' | 'bad' | 'warn' | 'info' | 'ok' | 'mute'
export const STATUS_FILTERS: ServiceStatusFilter[] = ['any', 'bad', 'warn', 'info', 'ok', 'mute']

export type ServiceSortKey = 'name' | 'status' | 'tag' | 'cluster' | 'account' | 'tasks' | 'revision' | 'image' | 'last'
export const SERVICE_SORT_KEYS: ServiceSortKey[] = ['name', 'status', 'tag', 'cluster', 'account', 'tasks', 'revision', 'image', 'last']
export type SortDir = 'asc' | 'desc'

/**
 * The one definition of "needs attention", shared by the Overview KPI, the Needs attention panel and the Problems view:
 * the service's status is failing, degraded, unhealthy or deploying, or it has an open alert (`attention`). Status is
 * included because the degraded alert is debounced (three polls) and a visibly Degraded service must never be missing
 * from Problems; the alert is included because some alerts (restart loop, failed deployment) sit on a steady service.
 * Scaled to zero is intentional and does not count.
 */
export const needsAttention = (service: Pick<ServiceListItemDTO, 'status' | 'attention'>): boolean =>
  service.attention !== '' ||
  service.status === 'failing' ||
  service.status === 'degraded' ||
  service.status === 'unhealthy_task' ||
  service.status === 'deploying'

export function applyView(items: ServiceListItemDTO[], view: ServiceView, pinnedIds: ReadonlySet<string>): ServiceListItemDTO[] {
  if (view === VIEW_PROBLEMS) return items.filter(needsAttention)
  if (view === VIEW_PINS) return items.filter((s) => pinnedIds.has(s.id))
  if (view.startsWith('tag-')) {
    const tagId = Number(view.slice('tag-'.length))
    return items.filter((s) => s.tag.id === tagId)
  }
  return items
}

export function applyStatus(items: ServiceListItemDTO[], status: ServiceStatusFilter): ServiceListItemDTO[] {
  return status === 'any' ? items : items.filter((s) => statusKind(s.status) === status)
}

export function statusCounts(items: ServiceListItemDTO[]): Record<ServiceStatusFilter, number> {
  const counts: Record<ServiceStatusFilter, number> = {
    any: items.length,
    bad: 0,
    warn: 0,
    info: 0,
    ok: 0,
    mute: 0,
  }
  for (const service of items) {
    const kind: PillKind = statusKind(service.status)
    if (kind in counts) counts[kind as ServiceStatusFilter] += 1
  }
  return counts
}

/** Case-insensitive substring over name, cluster, account, region, image tag and tag name. */
export function applyQuery(items: ServiceListItemDTO[], query: string): ServiceListItemDTO[] {
  const needle = query.trim().toLowerCase()
  if (!needle) return items
  return items.filter((s) =>
    [s.name, s.cluster.name, s.account_name, s.region, s.image_tag, s.tag.name].some((field) => field.toLowerCase().includes(needle)),
  )
}

/** Why Restart is unavailable for this row, or undefined when it is allowed. Same wording as the service header. */
export function restartBlockedReason(service: Pick<ServiceListItemDTO, 'can' | 'tag' | 'rollout'>): string | undefined {
  if (!service.can.operate) return `You need Operate on ${service.tag.name}`
  if (service.rollout === 'in_progress') return 'A rollout is already running'
  return undefined
}

/** `bad` when fewer tasks run than are desired. */
export function taskTone(service: Pick<ServiceListItemDTO, 'running' | 'desired'>): 'bad' | undefined {
  return service.running < service.desired ? 'bad' : undefined
}

/** The value a column sorts by; null sorts last in both directions. */
export function sortValue(service: ServiceListItemDTO, key: ServiceSortKey): string | number | null {
  switch (key) {
    case 'name':
      return service.name
    case 'status':
      return serviceSeverity(service.status) * 1000 + (service.desired - service.running)
    case 'tag':
      return service.tag.name
    case 'cluster':
      return service.cluster.name
    case 'account':
      return `${service.account_name} ${service.region}`
    case 'tasks':
      return service.desired === 0 ? 1 : service.running / service.desired
    case 'revision':
      return service.revision
    case 'image':
      return service.image_tag
    case 'last':
      return service.last_change_at ? Date.parse(service.last_change_at) : null
  }
}

const compareValues = (a: string | number, b: string | number): number =>
  typeof a === 'number' && typeof b === 'number'
    ? a - b
    : String(a).localeCompare(String(b), undefined, {
        numeric: true,
        sensitivity: 'base',
      })

/** A sorted copy; equal values fall back to the service name ascending, whatever the direction. */
export function sortServices(items: ServiceListItemDTO[], key: ServiceSortKey, dir: SortDir): ServiceListItemDTO[] {
  const factor = dir === 'asc' ? 1 : -1
  return [...items].sort((a, b) => {
    const av = sortValue(a, key)
    const bv = sortValue(b, key)
    if (av === null && bv !== null) return 1
    if (bv === null && av !== null) return -1
    const primary = av === null || bv === null ? 0 : factor * compareValues(av, bv)
    return primary !== 0
      ? primary
      : a.name.localeCompare(b.name, undefined, {
          numeric: true,
          sensitivity: 'base',
        })
  })
}
