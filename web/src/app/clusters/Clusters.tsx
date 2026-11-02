import { useClusters } from '../../api/hooks'
import { useClustersMetrics } from '../../api/observe'
import { EmptyState } from '../../components/EmptyState'
import { SegmentedFilter } from '../../components/Seg'
import { useCrumbs } from '../shell/crumbs'
import { useFilters } from '../shell/useFilters'
import { ClustersTable } from './ClustersTable'
import styles from './clusters.module.css'
import { clusterCounts, filterClusters } from './filterClusters'
import { useClusterFilters } from './useClusterFilters'

export function Clusters() {
  useCrumbs([['Clusters']])
  const { filters: scope } = useFilters()
  const clusters = useClusters(scope)
  const metrics = useClustersMetrics(scope)
  const items = clusters.data?.items ?? []
  const { filters, sort, page, setEnv, setStatus, setQ, setSort, setPage, clearAll } = useClusterFilters()
  const visible = filterClusters(items, filters)
  const counts = clusterCounts(items)

  const empty = (
    <div>
      No clusters match these filters.{' '}
      <button type="button" className={styles.btn} onClick={clearAll}>
        Clear filters
      </button>
    </div>
  )

  return (
    <div className={styles.page}>
      <div className={styles.pageHead}>
        <h1>Clusters</h1>
        <span className={styles.meta}>Created by Terraform. The environment label is stored only in Harbormaster.</span>
      </div>

      <div className={styles.filterBar} role="group" aria-label="Cluster filters">
        <SegmentedFilter
          label="Environment"
          value={filters.env}
          onChange={setEnv}
          options={[
            { value: 'all', label: 'All', count: counts.env.all },
            { value: 'prod', label: 'Prod', count: counts.env.prod },
            { value: 'staging', label: 'Staging', count: counts.env.staging },
            { value: 'dev', label: 'Dev', count: counts.env.dev },
          ]}
        />
        <SegmentedFilter
          label="Status"
          value={filters.status}
          onChange={setStatus}
          options={[
            { value: 'any', label: 'Any status', count: counts.status.any },
            { value: 'problems', label: 'Problems', count: counts.status.problems },
            { value: 'deploying', label: 'Deploying', count: counts.status.deploying },
            { value: 'healthy', label: 'Healthy', count: counts.status.healthy },
          ]}
        />
        <input
          className={styles.nameSearch}
          type="search"
          aria-label="Filter clusters"
          placeholder="Filter by cluster, account or region"
          value={filters.q}
          onChange={(event) => setQ(event.target.value)}
        />
        <button type="button" className={styles.btn} onClick={clearAll}>
          Clear filters
        </button>
      </div>

      {clusters.isError ? (
        <EmptyState>{clusters.error instanceof Error ? clusters.error.message : 'The clusters could not be loaded.'}</EmptyState>
      ) : (
        <ClustersTable
          clusters={visible}
          metricsById={metrics.data?.items ?? {}}
          sort={sort}
          onSortChange={setSort}
          page={page}
          onPageChange={setPage}
          empty={clusters.isSuccess ? empty : undefined}
        />
      )}
      <span className={styles.note}>A dash in CPU and memory means Container Insights is off for that cluster.</span>
    </div>
  )
}
