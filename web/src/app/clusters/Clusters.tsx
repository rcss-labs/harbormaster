import { useClusters } from '../../api/hooks'
import { useClustersMetrics } from '../../api/observe'
import { EmptyState } from '../../components/EmptyState'
import { Seg } from '../../components/Seg'
import { useCrumbs } from '../shell/crumbs'
import { useFilters } from '../shell/useFilters'
import { ClusterCard } from './ClusterCard'
import { ClustersTable } from './ClustersTable'
import styles from './clusters.module.css'
import { LIST_VIEW_OPTIONS, useListView } from './useListView'

/** Above this many clusters the table is the default view, since cards would run long. */
const TABLE_DEFAULT_THRESHOLD = 30

export function Clusters() {
  useCrumbs([['Clusters']])
  const { filters } = useFilters()
  const clusters = useClusters(filters)
  const metrics = useClustersMetrics(filters)
  const items = clusters.data?.items ?? []
  const metricsById = metrics.data?.items ?? {}
  const [view, setView] = useListView(items.length > TABLE_DEFAULT_THRESHOLD ? 'table' : 'cards')

  return (
    <>
      <div className={styles.pageHead}>
        <div className={styles.titles}>
          <h1>Clusters</h1>
          <div className={styles.meta}>Created by Terraform. The environment label is stored only in Harbormaster.</div>
        </div>
        <div className={styles.actions}>
          <Seg label="View" options={LIST_VIEW_OPTIONS} value={view} onChange={setView} />
        </div>
      </div>

      {clusters.isError ? (
        <EmptyState>{clusters.error instanceof Error ? clusters.error.message : 'The clusters could not be loaded.'}</EmptyState>
      ) : view === 'table' ? (
        <ClustersTable clusters={items} metricsById={metricsById} />
      ) : (
        <div className={styles.cards}>
          {items.map((cluster) => (
            <ClusterCard key={cluster.id} cluster={cluster} metrics={metricsById[cluster.id]} />
          ))}
        </div>
      )}
    </>
  )
}
