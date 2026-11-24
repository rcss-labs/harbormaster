import { useMemo, type CSSProperties } from 'react'
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

/** Up to this many clusters every berth is drawn with its name. Past it, only clusters with a problem keep theirs. */
const MAX_NAMED_BERTHS = 24
/** In a large estate, how many clusters of one region are named, so an outage does not flood the map. */
const MAX_NAMED_PER_REGION = 4

/** Worst first; the clusters that earn a name in a large estate. */
const PROBLEM_RANK: Partial<Record<ClusterStatus, number>> = { needs_attention: 0, degraded: 1 }

const LEGEND: ClusterStatus[] = ['healthy', 'deploying', 'degraded', 'needs_attention']

interface AccountRow {
  id: string
  name: string
  clusters: ClusterCardDTO[]
}

function byAccount(clusters: ClusterCardDTO[]): AccountRow[] {
  const rows = new Map<string, AccountRow>()
  for (const cluster of clusters) {
    const row = rows.get(cluster.account_id) ?? { id: cluster.account_id, name: cluster.account_name, clusters: [] }
    row.clusters.push(cluster)
    rows.set(cluster.account_id, row)
  }
  return [...rows.values()].sort((a, b) => a.name.localeCompare(b.name))
}

function byRegion(clusters: ClusterCardDTO[]): [string, ClusterCardDTO[]][] {
  const regions = new Map<string, ClusterCardDTO[]>()
  for (const cluster of clusters) regions.set(cluster.region, [...(regions.get(cluster.region) ?? []), cluster])
  return [...regions.entries()].sort(([a], [b]) => a.localeCompare(b))
}

const berthLabel = (cluster: ClusterCardDTO) =>
  `${cluster.name}, ${clusterStatusLabel(cluster.status)}, ${cluster.running}/${cluster.desired} tasks, ${cluster.region}`

/** One cluster at its berth. The load line at the foot fills to the share of desired tasks that are running. */
function Berth({ cluster }: { cluster: ClusterCardDTO }) {
  const load = cluster.desired > 0 ? Math.min(1, cluster.running / cluster.desired) : 0
  const label = berthLabel(cluster)
  const style = { '--berth': CLUSTER_CELL_COLOR[cluster.status], '--load': load } as CSSProperties
  return (
    <Link to={`/clusters/${cluster.id}`} className={styles.berth} data-status={cluster.status} style={style} aria-label={label} title={label}>
      <span className={styles.berthName}>{cluster.name}</span>
      <span className={styles.berthLoad}>
        {cluster.running}/{cluster.desired}
      </span>
      <span className={styles.loadLine} aria-hidden="true">
        <i />
      </span>
    </Link>
  )
}

/** The clusters of one region in a large estate: problems by name first, every other cluster as a cell. */
function splitBerths(clusters: ClusterCardDTO[]): { named: ClusterCardDTO[]; cells: ClusterCardDTO[] } {
  const named = clusters
    .filter((cluster) => PROBLEM_RANK[cluster.status] !== undefined)
    .sort((a, b) => PROBLEM_RANK[a.status]! - PROBLEM_RANK[b.status]! || a.name.localeCompare(b.name))
    .slice(0, MAX_NAMED_PER_REGION)
  return { named, cells: clusters.filter((cluster) => !named.includes(cluster)) }
}

function Cell({ cluster }: { cluster: ClusterCardDTO }) {
  const label = berthLabel(cluster)
  return <Link to={`/clusters/${cluster.id}`} className={styles.cell} style={{ background: CLUSTER_CELL_COLOR[cluster.status] }} aria-label={label} title={label} />
}

function Quay({ region, clusters, compact }: { region: string; clusters: ClusterCardDTO[]; compact: boolean }) {
  const { named, cells } = compact ? splitBerths(clusters) : { named: clusters, cells: [] }
  return (
    <div className={compact ? styles.quayCompact : styles.quay}>
      <span className={styles.quayName} aria-hidden="true">
        {region}
      </span>
      <ul className={styles.berths} aria-label={region}>
        {named.map((cluster) => (
          <li key={cluster.id}>
            <Berth cluster={cluster} />
          </li>
        ))}
        {cells.map((cluster) => (
          <li key={cluster.id}>
            <Cell cluster={cluster} />
          </li>
        ))}
      </ul>
    </div>
  )
}

function accountSummary(clusters: ClusterCardDTO[]): string {
  const failing = clusters.filter((cluster) => cluster.status === 'needs_attention').length
  const total = `${clusters.length} ${clusters.length === 1 ? 'cluster' : 'clusters'}`
  return failing > 0 ? `${total} · ${failing} ${failing === 1 ? 'needs' : 'need'} attention` : total
}

/**
 * The berth plan: one section per account, one quay per region, one berth per cluster. Accounts and regions keep
 * their place whatever their state, so the map can be read from memory.
 */
export function EstateMap() {
  const { filters } = useFilters()
  const clusters = useClusters(filters).data?.items
  const rows = useMemo(() => byAccount(clusters ?? []), [clusters])
  const compact = (clusters?.length ?? 0) > MAX_NAMED_BERTHS

  const legend = (
    <span className={styles.legend}>
      {LEGEND.map((status) => (
        <span key={status} className={styles.legendItem}>
          <i style={{ background: CLUSTER_CELL_COLOR[status] }} />
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
        <div className={compact ? styles.mapRowsCompact : styles.mapRows}>
          {rows.map((row) => (
            <div key={row.id} className={styles.mapRow} role="group" aria-label={row.name}>
              <div className={styles.mapName}>
                <span className={styles.mapAccount}>{row.name}</span>
                <span className={styles.mapSub}>{accountSummary(row.clusters)}</span>
              </div>
              <div className={compact ? styles.quaysCompact : styles.quays}>
                {byRegion(row.clusters).map(([region, berths]) => (
                  <Quay key={region} region={region} clusters={berths} compact={compact} />
                ))}
              </div>
            </div>
          ))}
        </div>
      )}
    </Panel>
  )
}
