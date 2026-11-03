import type { ClusterCardDTO, ClusterStatus } from '../../api/types'

/** 'all', or the id of a tag as a string. */
export type ClusterTagFilter = string
export const ALL_TAGS = 'all'
export type ClusterStatusFilter = 'any' | 'problems' | 'deploying' | 'healthy'

export interface ClusterPageFilters {
  tag: ClusterTagFilter
  status: ClusterStatusFilter
  q: string
}

/** Worst first when sorted descending: needs attention, degraded, deploying, healthy. */
const SEVERITY: Record<ClusterStatus, number> = { needs_attention: 3, degraded: 2, deploying: 1, healthy: 0 }
export const clusterSeverity = (status: ClusterStatus): number => SEVERITY[status]

/**
 * Resolves the ?env= URL value to a visible tag id. Links written before tags were configurable carry
 * prod/staging/dev; those map to the visible tag of that name. Anything else falls back to all.
 */
export function resolveTagFilter(param: string | null, visible: { id: number; name: string }[]): ClusterTagFilter {
  if (!param) return ALL_TAGS
  if (visible.some((tag) => String(tag.id) === param)) return param
  const byName = visible.find((tag) => tag.name.toLowerCase() === param.toLowerCase() && ['prod', 'staging', 'dev'].includes(param.toLowerCase()))
  return byName ? String(byName.id) : ALL_TAGS
}

export function statusGroup(status: ClusterStatus): Exclude<ClusterStatusFilter, 'any'> {
  if (status === 'needs_attention' || status === 'degraded') return 'problems'
  return status === 'deploying' ? 'deploying' : 'healthy'
}

/** Client-side narrowing; the text matches the cluster name, account or region as a trimmed, case-insensitive substring. */
export function filterClusters(items: ClusterCardDTO[], f: ClusterPageFilters): ClusterCardDTO[] {
  const needle = f.q.trim().toLowerCase()
  return items.filter(
    (cluster) =>
      (f.tag === ALL_TAGS || String(cluster.tag.id) === f.tag) &&
      (f.status === 'any' || statusGroup(cluster.status) === f.status) &&
      (needle === '' || `${cluster.name} ${cluster.account_name} ${cluster.region}`.toLowerCase().includes(needle)),
  )
}

export function clusterCounts(items: ClusterCardDTO[], visibleTags: { id: number }[]) {
  const tag: Record<string, number> = { [ALL_TAGS]: items.length }
  for (const { id } of visibleTags) tag[String(id)] = 0
  const status = { any: items.length, problems: 0, deploying: 0, healthy: 0 }
  for (const cluster of items) {
    const key = String(cluster.tag.id)
    if (key in tag) tag[key] += 1
    status[statusGroup(cluster.status)] += 1
  }
  return { tag, status }
}
