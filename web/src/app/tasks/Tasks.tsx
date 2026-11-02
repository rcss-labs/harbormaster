import { useState } from 'react'
import { useSearchParams } from 'react-router'
import { useTasks } from '../../api/hooks'
import type { TaskDTO, TasksFilter, TasksListDTO } from '../../api/types'
import { Donut } from '../../charts/Donut'
import { HBars } from '../../charts/HBars'
import { Legend } from '../../charts/Legend'
import { Card } from '../../components/Card'
import { Dialog } from '../../components/Dialog'
import { EmptyState } from '../../components/EmptyState'
import { Seg } from '../../components/Seg'
import { REASON_COLOR } from '../../components/status'
import { TaskTable } from '../../components/TaskTable'
import { usePagedCursor } from '../../components/usePagedCursor'
import { useCrumbs } from '../shell/crumbs'
import { HiddenStoppedNote } from './HiddenStoppedNote'
import { useFilters } from '../shell/useFilters'
import styles from './tasks.module.css'

const FILTERS: TasksFilter[] = ['problems', 'running', 'stopped', 'all']
const DEFAULT_FILTER: TasksFilter = 'problems'
const FARGATE_COLOR = 'var(--accent)'
const FARGATE_SPOT_COLOR = 'var(--series-3)'

const parseFilter = (value: string | null): TasksFilter => FILTERS.find((filter) => filter === value) ?? DEFAULT_FILTER

const TOP_CLUSTERS = 10

type ClusterCapacity = TasksListDTO['summary']['by_cluster'][number]
const totalRunning = (cluster: ClusterCapacity) => cluster.fargate + cluster.spot

/** Busiest first; clusters with the same number of tasks fall back to name order. */
const byBusiest = (clusters: ClusterCapacity[]) =>
  [...clusters].sort((a, b) => totalRunning(b) - totalRunning(a) || a.name.localeCompare(b.name))

function ClusterBars({ clusters }: { clusters: ClusterCapacity[] }) {
  return (
    <>
      <HBars
        items={clusters.map((cluster) => ({
          name: cluster.name,
          href: `/clusters/${cluster.id}`,
          parts: [
            { v: cluster.fargate, color: FARGATE_COLOR },
            { v: cluster.spot, color: FARGATE_SPOT_COLOR },
          ],
          label: String(totalRunning(cluster)),
        }))}
      />
      <div className={styles.legendRow}>
        <Legend
          items={[
            { name: 'Fargate', color: FARGATE_COLOR },
            { name: 'Fargate Spot', color: FARGATE_SPOT_COLOR },
          ]}
        />
      </div>
    </>
  )
}

/** Seg label with its count in muted text, as in the mockup. */
const withCount = (label: string, count: number | undefined) => (
  <>
    {label} {count !== undefined && <span className={styles.muted}>{count}</span>}
  </>
)

export function Tasks() {
  useCrumbs([['Tasks']])
  const { filters } = useFilters()
  const [searchParams, setSearchParams] = useSearchParams()
  const filter = parseFilter(searchParams.get('filter'))
  // Earlier pages reset whenever the filter or the account/region scope changes.
  const paged = usePagedCursor<TaskDTO>(`${filter}|${filters.account ?? ''}|${filters.region ?? ''}`)
  const page = useTasks({ ...filters, filter, cursor: paged.cursor })
  const data = page.data
  const summary = data?.summary
  const [viewingAll, setViewingAll] = useState(false)
  const busiestClusters = byBusiest(summary?.by_cluster ?? [])

  const setFilter = (value: TasksFilter) =>
    setSearchParams(
      (current) => {
        const next = new URLSearchParams(current)
        next.set('filter', value)
        return next
      },
      { replace: true },
    )

  return (
    <>
      <div className={styles.pageHead}>
        <div className={styles.titles}>
          <h1>Tasks</h1>
          <div className={styles.meta}>
            Every task in every cluster you can see. ECS keeps stopped tasks for about an hour; Harbormaster keeps them for 7
            days.
          </div>
        </div>
      </div>

      {summary && (
        <div className={styles.grid}>
          <Card title="By status">
            <Donut
              segments={[
                { name: 'Running, healthy', v: summary.by_status.running_healthy, color: 'var(--success)' },
                { name: 'Running, unhealthy', v: summary.by_status.running_unhealthy, color: 'var(--warning)' },
                { name: 'Stopped, error', v: summary.by_status.stopped_error, color: 'var(--danger)' },
                { name: 'Stopped, normal', v: summary.by_status.stopped_normal, color: 'var(--border-strong)' },
              ]}
              label={String(summary.by_status.running_healthy + summary.by_status.running_unhealthy)}
              sub="running"
            />
          </Card>
          <Card title="Running by availability zone">
            <HBars items={summary.by_az.map((zone) => ({ name: zone.az, v: zone.count, color: 'var(--accent)' }))} />
          </Card>
          <Card title="Running by cluster">
            <ClusterBars clusters={busiestClusters.slice(0, TOP_CLUSTERS)} />
            {busiestClusters.length > TOP_CLUSTERS && (
              <div className={styles.viewAll}>
                <button type="button" className={styles.btn} onClick={() => setViewingAll(true)}>
                  {`View all (${busiestClusters.length})`}
                </button>
              </div>
            )}
          </Card>
          <Card title="Why tasks stopped">
            <HBars
              items={summary.stop_reasons.map((reason) => ({
                name: reason.reason,
                v: reason.count,
                color: REASON_COLOR[reason.reason],
              }))}
            />
          </Card>
        </div>
      )}

      <div className={styles.toolbar}>
        <Seg
          label="Task filter"
          value={filter}
          onChange={setFilter}
          options={[
            { value: 'problems', label: withCount('Problems first', data?.counts.problems) },
            { value: 'running', label: withCount('Running', data?.counts.running) },
            { value: 'stopped', label: withCount('Stopped', data?.counts.stopped) },
            { value: 'all', label: withCount('All', data?.counts.all) },
          ]}
        />
        <HiddenStoppedNote />
      </div>

      {page.isError ? (
        <EmptyState>{page.error instanceof Error ? page.error.message : 'The tasks could not be loaded.'}</EmptyState>
      ) : (
        <TaskTable
          items={[...paged.earlier, ...(data?.items ?? [])]}
          withService
          grouped
          next={data?.next ?? undefined}
          onMore={() => {
            if (data?.next) paged.loadMore(data.items, data.next)
          }}
        />
      )}

      {viewingAll && (
        <Dialog title="Running by cluster" actions={[{ label: 'Close', onClick: () => setViewingAll(false) }]} onClose={() => setViewingAll(false)}>
          <ClusterBars clusters={busiestClusters} />
        </Dialog>
      )}
    </>
  )
}
