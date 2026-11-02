import type { ClusterCardDTO, ClusterStatus } from '../../api/types'

export interface ClusterPageFilters {
  status: ClusterStatus[]
  q: string
}

/** Client-side narrowing: an empty status list means every status; the name is a trimmed, case-insensitive substring. */
export function filterClusters(items: ClusterCardDTO[], f: ClusterPageFilters): ClusterCardDTO[] {
  const needle = f.q.trim().toLowerCase()
  return items.filter(
    (cluster) =>
      (f.status.length === 0 || f.status.includes(cluster.status)) && (needle === '' || cluster.name.toLowerCase().includes(needle)),
  )
}
