import { useDeferredValue, useMemo } from 'react'
import { useSearchParams } from 'react-router'
import { useSettings } from '../../api/hooks'
import type { TasksFilter, TasksListDTO } from '../../api/types'
import { ColumnPicker, useColumnPrefs } from '../../components/ColumnPicker'
import { EmptyState } from '../../components/EmptyState'
import { KpiStrip, type KpiItem } from '../../components/KpiStrip'
import { SegmentedFilter } from '../../components/Seg'
import { TASK_COLUMNS } from '../../components/TaskTable'
import { Toggle } from '../../components/Toggle'
import { useCrumbs } from '../shell/crumbs'
import { useFilters } from '../shell/useFilters'
import { HiddenStoppedNote } from './HiddenStoppedNote'
import { buildRows, inSegment, matchesQuery } from './taskRows'
import styles from './tasks.module.css'
import { useAllTasks } from './useAllTasks'
import { useTaskPanel } from './useTaskPanel'
import { TasksTable } from './TasksTable'

const COLUMN_DEFAULTS = TASK_COLUMNS.map((column) => column.key)

const FILTERS: TasksFilter[] = ['problems', 'running', 'stopped', 'all']
const DEFAULT_FILTER: TasksFilter = 'problems'
const parseFilter = (value: string | null): TasksFilter => FILTERS.find((filter) => filter === value) ?? DEFAULT_FILTER
const parsePage = (value: string | null) => Math.max(1, Math.floor(Number(value)) || 1)

/** A KPI value with a small caption next to it. */
const withCaption = (value: string | number, caption: string) => (
  <>
    {value} <span className={styles.caption}>{caption}</span>
  </>
)

function kpiItems(summary: TasksListDTO['summary'], windowMinutes: number | undefined): KpiItem[] {
  const { running_healthy: healthy, running_unhealthy: unhealthy, stopped_error: stoppedError, stopped_normal: stoppedNormal, stopped_expected: stoppedExpected } = summary.by_status
  const running = healthy + unhealthy
  const spot = summary.by_cluster.reduce((total, cluster) => total + cluster.spot, 0)
  const stoppedCaption = windowMinutes === undefined ? 'recent' : `last ${windowMinutes} min`
  return [
    { label: 'Running', value: running },
    { label: 'Unhealthy', value: unhealthy, valueColor: unhealthy > 0 ? 'var(--warning)' : undefined },
    { label: 'Stopped, error', value: withCaption(stoppedError, stoppedCaption), valueColor: stoppedError > 0 ? 'var(--danger)' : undefined },
    { label: 'Stopped, normal', value: withCaption(stoppedNormal, stoppedCaption) },
    { label: 'Stopped, expected', value: withCaption(stoppedExpected, stoppedCaption), valueColor: stoppedExpected > 0 ? 'var(--expected)' : undefined },
    { label: 'On spot', value: withCaption(spot, running > 0 ? `${Math.round((spot / running) * 100)}% of running` : '') },
  ]
}

export function Tasks() {
  useCrumbs([['Tasks']])
  const { filters } = useFilters()
  const [searchParams, setSearchParams] = useSearchParams()
  const segment = parseFilter(searchParams.get('filter'))
  const query = searchParams.get('q') ?? ''
  const group = searchParams.get('group') !== '0'
  const page = parsePage(searchParams.get('page'))
  // Group keys never contain a newline, so one string stands in for the list and keeps memoisation simple.
  const openParam = searchParams.getAll('open').join('\n')

  const all = useAllTasks(filters)
  const windowMinutes = useSettings().data?.stopped_tasks_window_minutes
  const deferredQuery = useDeferredValue(query)
  const [visibleColumns, setVisibleColumns] = useColumnPrefs('harbormaster-columns:tasks', COLUMN_DEFAULTS)
  const { onOpen, onStop, panel } = useTaskPanel(all.items)

  const matching = useMemo(() => all.items.filter((task) => matchesQuery(task, deferredQuery)), [all.items, deferredQuery])
  const counts = useMemo(
    () => Object.fromEntries(FILTERS.map((filter) => [filter, matching.filter((task) => inSegment(task, filter)).length])) as Record<TasksFilter, number>,
    [matching],
  )
  const openSet = useMemo(() => new Set(openParam ? openParam.split('\n') : []), [openParam])
  const rows = useMemo(
    () =>
      buildRows(
        matching.filter((task) => inSegment(task, segment)),
        group,
        openSet,
      ),
    [matching, segment, group, openSet],
  )

  const update = (mutate: (params: URLSearchParams) => void) =>
    setSearchParams(
      (current) => {
        const next = new URLSearchParams(current)
        mutate(next)
        return next
      },
      { replace: true },
    )
  /** Changing what the list holds starts again at page one. */
  const resetPage = (params: URLSearchParams) => params.delete('page')

  const setSegment = (value: TasksFilter) =>
    update((params) => {
      params.set('filter', value)
      resetPage(params)
    })
  const setQuery = (value: string) =>
    update((params) => {
      if (value) params.set('q', value)
      else params.delete('q')
      resetPage(params)
    })
  const setGroup = (on: boolean) =>
    update((params) => {
      if (on) params.delete('group')
      else params.set('group', '0')
      resetPage(params)
    })
  const setPage = (value: number) =>
    update((params) => {
      if (value > 1) params.set('page', String(value))
      else params.delete('page')
    })
  const toggleGroup = (groupKey: string) =>
    update((params) => {
      const open = params.getAll('open')
      params.delete('open')
      const next = open.includes(groupKey) ? open.filter((key) => key !== groupKey) : [...open, groupKey]
      for (const key of next) params.append('open', key)
    })

  const failed = all.error && all.items.length === 0

  return (
    <>
      <div className={styles.pageHead}>
        <h1>Tasks</h1>
        <span className={styles.meta}>
          Every task in every cluster you can see. ECS keeps stopped tasks for about an hour; Harbormaster keeps them for 7 days.
        </span>
      </div>

      {all.summary && <KpiStrip label="Task summary" items={kpiItems(all.summary, windowMinutes)} />}

      <div className={styles.toolbar}>
        <SegmentedFilter
          label="Task filter"
          value={segment}
          onChange={setSegment}
          options={[
            { value: 'problems', label: 'Problems', count: counts.problems },
            { value: 'running', label: 'Running', count: counts.running },
            { value: 'stopped', label: 'Stopped', count: counts.stopped },
            { value: 'all', label: 'All', count: counts.all },
          ]}
        />
        <input
          type="search"
          className={styles.search}
          aria-label="Filter tasks"
          data-hm-filter
          placeholder="Filter by task ID, service, cluster, IP"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
        <Toggle checked={group} onChange={setGroup}>
          Group repeats
        </Toggle>
        <HiddenStoppedNote />
        <span className={styles.spacer} />
        <ColumnPicker columns={TASK_COLUMNS} visible={visibleColumns} onChange={setVisibleColumns} defaults={COLUMN_DEFAULTS} />
      </div>

      {failed ? (
        <EmptyState>{all.error instanceof Error ? all.error.message : 'The tasks could not be loaded.'}</EmptyState>
      ) : all.loading && all.items.length === 0 ? (
        <EmptyState>Loading tasks…</EmptyState>
      ) : (
        <>
          <TasksTable rows={rows} page={page} onPageChange={setPage} onToggleGroup={toggleGroup}
            onOpen={onOpen}
            onStop={onStop}
            visibleColumns={visibleColumns}
            empty="No tasks match." />
          {all.truncated && (
            <div className={styles.note}>
              {`Showing the first ${all.items.length} tasks. Filter by service, cluster or account to see the rest.`}
            </div>
          )}
        </>
      )}
      {panel}
    </>
  )
}
