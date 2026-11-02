import { useMemo } from 'react'
import { Link } from 'react-router'
import { useClusters } from '../../api/hooks'
import { allNull, useOverviewMetrics } from '../../api/observe'
import type { ClusterSeriesDTO, OverviewMetricsDTO } from '../../api/types'
import { Banner } from '../../components/Banner'
import { EmptyState } from '../../components/EmptyState'
import { Panel } from '../../components/Panel'
import { StaleNote } from '../../components/StaleNote'
import { useFilters } from '../shell/useFilters'
import styles from './overview.module.css'

const INSIGHTS_OFF_TEXT = 'Container Insights is off for every cluster you can see.'
const BUSIEST_COUNT = 6
/** CPU above this share is drawn in the warning colour. */
const HOT_CPU_PERCENT = 55

/** Per-index mean over the clusters that have a datapoint there; gaps with no datapoint at all are dropped. */
function meanSeries(clusters: ClusterSeriesDTO[]): number[] {
  const pointCount = Math.max(0, ...clusters.map((cluster) => cluster.values.length))
  const means: number[] = []
  for (let i = 0; i < pointCount; i++) {
    const samples = clusters.map((cluster) => cluster.values[i]).filter((v): v is number => v !== undefined && v !== null)
    if (samples.length > 0) means.push(samples.reduce((total, v) => total + v, 0) / samples.length)
  }
  return means
}

/** The newest datapoint a cluster has reported, if any. */
function latestValue(series: ClusterSeriesDTO): number | null {
  for (let i = series.values.length - 1; i >= 0; i--) {
    const value = series.values[i]
    if (value !== null && value !== undefined) return value
  }
  return null
}

/** Average CPU and memory over every service, with the mean cluster curve as the sparkline source. */
export function useUsageKpis() {
  const { filters } = useFilters()
  const data = useOverviewMetrics(filters).data
  return {
    cpu: data?.avg.cpu ?? null,
    mem: data?.avg.mem ?? null,
    cpuSpark: meanSeries(data?.cpu_by_cluster ?? []),
    memSpark: meanSeries(data?.mem_by_cluster ?? []),
  }
}

/** Names the account x regions that could not be read, so a partial list is not mistaken for a full one. */
function RegionErrors({ data }: { data?: OverviewMetricsDTO }) {
  const regions = [...new Set((data?.errors ?? []).map((error) => error.region))]
  if (regions.length === 0) return null
  return <Banner kind="warn">CloudWatch could not be read for {regions.join(', ')}; those clusters are missing from this list.</Banner>
}

/** The clusters with the highest latest CPU, drawn as bars. */
export function BusiestClusters() {
  const { filters } = useFilters()
  const data = useOverviewMetrics(filters).data
  const clusterList = useClusters(filters).data?.items
  const where = useMemo(
    () => new Map((clusterList ?? []).map((cluster) => [cluster.id, `${cluster.account_name} · ${cluster.region}`])),
    [clusterList],
  )

  const busiest = useMemo(() => {
    const reporting = (data?.cpu_by_cluster ?? [])
      .filter((series) => series.label !== 'others')
      .map((series) => ({ series, cpu: latestValue(series) }))
      .filter((entry): entry is { series: ClusterSeriesDTO; cpu: number } => entry.cpu !== null)
    return reporting.sort((a, b) => b.cpu - a.cpu).slice(0, BUSIEST_COUNT)
  }, [data])

  const noInsights =
    !data || data.timestamps.length === 0 || data.cpu_by_cluster.length === 0 || allNull(...data.cpu_by_cluster.map((series) => series.values))

  return (
    <Panel title="Busiest clusters, CPU" actions={<Link to="/clusters">All clusters →</Link>} flush>
      <div className={styles.panelNotes}>
        {data?.stale && <StaleNote stale={data.stale} />}
        <RegionErrors data={data} />
      </div>
      {noInsights ? (
        <EmptyState>{data ? INSIGHTS_OFF_TEXT : 'No data yet'}</EmptyState>
      ) : (
        busiest.map(({ series, cpu }) => (
          <Link key={series.id} to={`/clusters/${series.id}`} className={styles.busyRow}>
            <span className={styles.busyName}>
              <span className={styles.busyCluster}>{series.label}</span>
              <span className={styles.where}>{where.get(series.id) ?? ''}</span>
            </span>
            <span className={styles.busyTrack} aria-hidden="true">
              <span
                className={styles.busyFill}
                style={{ width: `${Math.min(100, Math.max(0, cpu))}%`, background: cpu > HOT_CPU_PERCENT ? 'var(--warning)' : 'var(--accent)' }}
              />
            </span>
            <span className={styles.busyValue}>{Math.round(cpu)}%</span>
          </Link>
        ))
      )}
    </Panel>
  )
}
