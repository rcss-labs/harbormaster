import type { ClusterStatus } from '../../api/types'
import { clusterStatusLabel } from '../../components/status'
import { Filters } from '../shell/Filters'
import styles from './clusters.module.css'
import type { ClusterPageFilters } from './filterClusters'
import { CLUSTER_STATUSES } from './useClusterFilters'

interface ClusterFiltersProps {
  page: ClusterPageFilters
  onStatus: (status: ClusterStatus[]) => void
  onQ: (q: string) => void
  onClear: () => void
}

export function ClusterFilters({ page, onStatus, onQ, onClear }: ClusterFiltersProps) {
  const toggle = (status: ClusterStatus) =>
    onStatus(page.status.includes(status) ? page.status.filter((s) => s !== status) : [...page.status, status])
  return (
    <div className={styles.filterBar} role="group" aria-label="Cluster filters">
      <div className={styles.statusToggles} role="group" aria-label="Status filter">
        {CLUSTER_STATUSES.map((status) => {
          const on = page.status.includes(status)
          return (
            <button key={status} type="button" className={on ? styles.statusOn : ''} aria-pressed={on} onClick={() => toggle(status)}>
              {clusterStatusLabel(status)}
            </button>
          )
        })}
      </div>
      <input
        className={styles.nameSearch}
        type="search"
        aria-label="Cluster name"
        placeholder="Search clusters"
        value={page.q}
        onChange={(e) => onQ(e.target.value)}
      />
      <Filters variant="page" />
      <button type="button" className={styles.btn} onClick={onClear}>
        Clear filters
      </button>
    </div>
  )
}
