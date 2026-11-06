import { useAuth } from '../auth/AuthProvider'
import { useClusters } from '../../api/hooks'
import { useClustersMetrics } from '../../api/observe'
import { ColumnPicker, useColumnPrefs } from '../../components/ColumnPicker'
import { DataBanner } from '../../components/DataBanner'
import { EmptyState } from '../../components/EmptyState'
import { Chips } from '../../components/Chips'
import { SegmentedFilter } from '../../components/Seg'
import { SortFieldsButton } from '../../components/SortFieldsButton'
import { usePhone } from '../../components/useNarrow'
import { useCrumbs } from '../shell/crumbs'
import { useFilters, visibleTagIds } from '../shell/useFilters'
import { CLUSTER_COLUMN_OPTIONS, ClustersTable } from './ClustersTable'
import styles from './clusters.module.css'
import { ALL_TAGS, clusterCounts, filterClusters } from './filterClusters'
import { DEFAULT_SORT, useClusterFilters } from './useClusterFilters'

/** More tags than this would overflow the segmented control, so a select is used instead. */
const MAX_TAG_SEGMENTS = 5

/** Sort keys offered by the phone sort sheet, in the order of the desktop column headers. */
const CLUSTER_SORT_OPTIONS = [
  { key: 'name', label: 'Cluster' },
  { key: 'status', label: 'Status' },
  { key: 'services', label: 'Services' },
  { key: 'tasks', label: 'Tasks' },
  { key: 'cpu', label: 'CPU 24h' },
  { key: 'mem', label: 'Memory 24h' },
]

export function Clusters() {
  useCrumbs([['Clusters']])
  const { filters: scope } = useFilters()
  const clusters = useClusters(scope)
  const metrics = useClustersMetrics(scope)
  const items = clusters.data?.items ?? []
  const { filters, sort, page, setTag, setStatus, setQ, setSort, setPage, clearAll } = useClusterFilters()
  const [visibleColumns, setVisibleColumns] = useColumnPrefs(
    'harbormaster-columns:clusters',
    CLUSTER_COLUMN_OPTIONS.map((column) => column.key),
  )
  const visible = filterClusters(items, filters)
  const phone = usePhone()
  const { me } = useAuth()
  // Same source and order as the top-bar tag filter.
  const visibleTags = me ? visibleTagIds(me).flatMap((id) => me.tags.find((t) => t.id === id) ?? []) : []
  const counts = clusterCounts(items, visibleTags)
  const tagOptions = [
    { value: ALL_TAGS, label: 'All', count: counts.tag[ALL_TAGS] },
    ...visibleTags.map((tag) => ({ value: String(tag.id), label: tag.name, count: counts.tag[String(tag.id)] })),
  ]

  const statusOptions = [
    { value: 'any' as const, label: 'Any status', count: counts.status.any },
    { value: 'problems' as const, label: 'Problems', count: counts.status.problems },
    { value: 'deploying' as const, label: 'Deploying', count: counts.status.deploying },
    { value: 'healthy' as const, label: 'Healthy', count: counts.status.healthy },
  ]
  const sortLabel = CLUSTER_SORT_OPTIONS.find((option) => option.key === sort.key)?.label ?? ''

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
        <h1 className="hm-phone-title-sr">Clusters</h1>
        <span className={styles.meta}>Created by Terraform. The tag is stored only in Harbormaster.</span>
      </div>
      <DataBanner tiers={['Clusters and services', 'Tasks']} />

      {phone ? (
        <div className={styles.phoneFilters} role="group" aria-label="Cluster filters">
          <Chips label="Tag" options={tagOptions} value={filters.tag} onChange={setTag} />
          <Chips label="Status" options={statusOptions} value={filters.status} onChange={setStatus} />
          <div className={styles.phoneSearchRow}>
            <input
              className={styles.nameSearch}
              type="search"
              aria-label="Filter clusters"
              data-hm-filter
              placeholder="Filter by cluster, account or region"
              value={filters.q}
              onChange={(event) => setQ(event.target.value)}
            />
            <SortFieldsButton
              sortOptions={CLUSTER_SORT_OPTIONS}
              sort={sort}
              onSortChange={setSort}
              columns={CLUSTER_COLUMN_OPTIONS}
              visible={visibleColumns}
              onVisibleChange={setVisibleColumns}
              defaults={{ sort: DEFAULT_SORT, visible: CLUSTER_COLUMN_OPTIONS.map((column) => column.key) }}
            />
          </div>
        </div>
      ) : (
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
          data-hm-filter
          placeholder="Filter by cluster, account or region"
          value={filters.q}
          onChange={(event) => setQ(event.target.value)}
        />
        <button type="button" className={styles.btn} onClick={clearAll}>
          Clear filters
        </button>
        <ColumnPicker columns={CLUSTER_COLUMN_OPTIONS} visible={visibleColumns} onChange={setVisibleColumns} />
      </div>
      )}

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
          visibleColumns={visibleColumns}
        />
      )}
      {phone && (
        <span className={styles.note}>{`${visible.length} of ${items.length} clusters · Sorted by ${sortLabel.toLowerCase()}${sort.dir === 'desc' ? ', descending' : ''}`}</span>
      )}
      <span className={styles.note}>A dash in CPU and memory means Container Insights is off for that cluster.</span>
    </div>
  )
}
