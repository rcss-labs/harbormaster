import type { ClusterCardDTO, ClusterStatus } from '../../api/types'

export type ClusterEnv = 'all' | 'prod' | 'staging' | 'dev'
export type ClusterStatusFilter = 'any' | 'problems' | 'deploying' | 'healthy'

export interface ClusterPageFilters {
  env: ClusterEnv
  status: ClusterStatusFilter
  q: string
}

/** Worst first when sorted descending: needs attention, degraded, deploying, healthy. */
const SEVERITY: Record<ClusterStatus, number> = { needs_attention: 3, degraded: 2, deploying: 1, healthy: 0 }
export const clusterSeverity = (status: ClusterStatus): number => SEVERITY[status]

/** The environment is the cluster's tag when it is Prod, Staging or Dev; custom tags have no environment. */
export function clusterEnv(cluster: ClusterCardDTO): Exclude<ClusterEnv, 'all'> | null {
  const name = cluster.tag.name.toLowerCase()
  return name === 'prod' || name === 'staging' || name === 'dev' ? name : null
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
      (f.env === 'all' || clusterEnv(cluster) === f.env) &&
      (f.status === 'any' || statusGroup(cluster.status) === f.status) &&
      (needle === '' || `${cluster.name} ${cluster.account_name} ${cluster.region}`.toLowerCase().includes(needle)),
  )
}

export function clusterCounts(items: ClusterCardDTO[]) {
  const env = { all: items.length, prod: 0, staging: 0, dev: 0 }
  const status = { any: items.length, problems: 0, deploying: 0, healthy: 0 }
  for (const cluster of items) {
    const e = clusterEnv(cluster)
    if (e) env[e] += 1
    status[statusGroup(cluster.status)] += 1
  }
  return { env, status }
}
