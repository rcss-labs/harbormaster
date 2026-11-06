import { useMemo } from 'react'
import { Link } from 'react-router'
import { useClusters } from '../../api/hooks'
import type { ClusterCardDTO, ClusterStatus } from '../../api/types'
import { EmptyState } from '../../components/EmptyState'
import { Panel } from '../../components/Panel'
import { clusterStatusLabel } from '../../components/status'
import { useFilters } from '../shell/useFilters'
import styles from './overview.module.css'

export const CLUSTER_CELL_COLOR: Record<ClusterStatus, string> = {
  healthy: 'var(--cell-ok)',
  deploying: 'var(--info)',
  degraded: 'var(--warn)',
  needs_attention: 'var(--bad)',
}

const LEGEND: ClusterStatus[] = ['healthy', 'deploying', 'degraded', 'needs_attention']

interface AccountRow {
  id: string
  name: string
  regions: string[]
  clusters: ClusterCardDTO[]
}

function byAccount(clusters: ClusterCardDTO[]): AccountRow[] {
  const rows = new Map<string, AccountRow>()
  for (const cluster of clusters) {
    const row = rows.get(cluster.account_id) ?? { id: cluster.account_id, name: cluster.account_name, regions: [], clusters: [] }
    if (!row.regions.includes(cluster.region)) row.regions.push(cluster.region)
    row.clusters.push(cluster)
    rows.set(cluster.account_id, row)
  }
  return [...rows.values()].sort((a, b) => a.name.localeCompare(b.name))
}

/** One row per account, one 16px cell per cluster coloured by status. */
export function EstateMap() {
  const { filters } = useFilters()
  const clusters = useClusters(filters).data?.items
  const rows = useMemo(() => byAccount(clusters ?? []), [clusters])

  const legend = (
    <span className={styles.legend}>
      {LEGEND.map((status) => (
        <span key={status} className={styles.legendItem}>
          <i className={status === 'healthy' ? styles.cellOk : undefined} style={{ background: CLUSTER_CELL_COLOR[status] }} />
          {clusterStatusLabel(status)}
        </span>
      ))}
    </span>
  )

  return (
    <Panel title="Estate map" actions={legend}>
      {rows.length === 0 ? (
        <EmptyState>{clusters ? 'No clusters.' : 'Loading clusters.'}</EmptyState>
      ) : (
        <div className={styles.mapRows}>
          {rows.map((row) => {
            const failing = row.clusters.filter((cluster) => cluster.status === 'needs_attention').length
            return (
              <div key={row.id} className={styles.mapRow} role="group" aria-label={row.name}>
                <div className={styles.mapName}>
                  <span className={styles.mapAccount}>{row.name}</span>
                  <span className={styles.mapSub}>
                    {row.regions.join(' ')} · {row.clusters.length}
                    {failing > 0 ? ` · ${failing} need attention` : ''}
                  </span>
                </div>
                <div className={styles.cells}>
                  {row.clusters.map((cluster) => {
                    const label = `${cluster.name}, ${clusterStatusLabel(cluster.status)}, ${cluster.running}/${cluster.desired} tasks, ${cluster.region}`
                    return (
                      <Link
                        key={cluster.id}
                        to={`/clusters/${cluster.id}`}
                        className={`${styles.cell} ${cluster.status === 'healthy' ? styles.cellOk : ''}`.trim()}
                        style={{ background: CLUSTER_CELL_COLOR[cluster.status] }}
                        aria-label={label}
                        title={label}
                      />
                    )
                  })}
                </div>
              </div>
            )
          })}
        </div>
      )}
    </Panel>
  )
}
