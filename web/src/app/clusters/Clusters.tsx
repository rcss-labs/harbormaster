import { useAuth } from '../auth/AuthProvider'
import { useClusters } from '../../api/hooks'
import { useClustersMetrics } from '../../api/observe'
import { EmptyState } from '../../components/EmptyState'
import { SegmentedFilter } from '../../components/Seg'
import { useCrumbs } from '../shell/crumbs'
import { useFilters, visibleTagIds } from '../shell/useFilters'
import { ClustersTable } from './ClustersTable'
import styles from './clusters.module.css'
import { ALL_TAGS, clusterCounts, filterClusters } from './filterClusters'
import { useClusterFilters } from './useClusterFilters'

/** More tags than this would overflow the segmented control, so a select is used instead. */
const MAX_TAG_SEGMENTS = 5

export function Clusters() {
  useCrumbs([['Clusters']])
  const { filters: scope } = useFilters()
  const clusters = useClusters(scope)
  const metrics = useClustersMetrics(scope)
  const items = clusters.data?.items ?? []
  const { filters, sort, page, setTag, setStatus, setQ, setSort, setPage, clearAll } = useClusterFilters()
  const visible = filterClusters(items, filters)
  const { me } = useAuth()
  // Same source and order as the top-bar tag filter.
  const visibleTags = me ? visibleTagIds(me).flatMap((id) => me.tags.find((t) => t.id === id) ?? []) : []
  const counts = clusterCounts(items, visibleTags)
  const tagOptions = [
    { value: ALL_TAGS, label: 'All', count: counts.tag[ALL_TAGS] },
    ...visibleTags.map((tag) => ({ value: String(tag.id), label: tag.name, count: counts.tag[String(tag.id)] })),
  ]

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
        <span className={styles.meta}>Created by Terraform. The tag is stored only in Harbormaster.</span>
      </div>

      <div className={styles.filterBar} role="group" aria-label="Cluster filters">
        {tagOptions.length > MAX_TAG_SEGMENTS + 1 ? (
          <select className={styles.tagSelect} aria-label="Tag" value={filters.tag} onChange={(event) => setTag(event.target.value)}>
            {tagOptions.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label} {option.count}
              </option>
            ))}
          </select>
        ) : (
          <SegmentedFilter label="Tag" value={filters.tag} onChange={setTag} options={tagOptions} />
        )}
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
