import { allNull, useOverviewMetrics } from '../../api/observe'
import type { ClusterSeriesDTO, OverviewMetricsDTO } from '../../api/types'
import { Banner } from '../../components/Banner'
import { Tile } from '../../components/Tile'
import { SERIES } from '../../components/status'
import { Legend } from '../../charts/Legend'
import { LineChart } from '../../charts/LineChart'
import { Sparkline } from '../../charts/Sparkline'
import { XL24 } from '../../charts/format'
import { MetricPanel } from '../observe/MetricPanel'
import { useFilters } from '../shell/useFilters'
import styles from './overview.module.css'

const OTHERS_COLOR = 'var(--border-strong)'
const INSIGHTS_OFF_TEXT = 'Container Insights is off for every cluster you can see.'

const clusterColor = (series: ClusterSeriesDTO, index: number) => (series.label === 'others' ? OTHERS_COLOR : SERIES[index % SERIES.length])

const percentText = (value: number | null) => (value === null ? '–' : `${Math.round(value)}%`)

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

/** Names the account x regions that could not be read, so a partial chart is not mistaken for a full one. */
function RegionErrors({ data }: { data?: OverviewMetricsDTO }) {
  const regions = [...new Set((data?.errors ?? []).map((error) => error.region))]
  if (regions.length === 0) return null
  return <Banner kind="warn">CloudWatch could not be read for {regions.join(', ')}; those clusters are missing from this chart.</Banner>
}

const hasNoInsights = (data: OverviewMetricsDTO, clusters: ClusterSeriesDTO[]) =>
  clusters.length === 0 || allNull(...clusters.map((cluster) => cluster.values)) || data.timestamps.length === 0

/** The 4th overview tile: average CPU and memory over every service, with a sparkline of the mean CPU. */
export function AverageUseTile() {
  const { filters } = useFilters()
  const data = useOverviewMetrics(filters).data
  const avg = data?.avg ?? { cpu: null, mem: null }
  return (
    <Tile
      k="Average use, all services"
      v={
        <>
          {percentText(avg.cpu)}
          <small>CPU</small> {percentText(avg.mem)}
          <small>memory</small>
        </>
      }
    >
      <Sparkline values={meanSeries(data?.cpu_by_cluster ?? [])} color="var(--series-1)" height={24} fluid />
    </Tile>
  )
}

function ClusterLines({ data, clusters, height, withLegend }: { data: OverviewMetricsDTO; clusters: ClusterSeriesDTO[]; height: number; withLegend: boolean }) {
  // Colours follow the CPU list order so a cluster keeps its colour in both charts.
  const order = data.cpu_by_cluster.map((cluster) => cluster.id)
  const colored = clusters.map((cluster, i) => {
    const position = order.indexOf(cluster.id)
    return { name: cluster.label, color: clusterColor(cluster, position >= 0 ? position : i), values: cluster.values }
  })
  return (
    <>
      {withLegend && (
        <div className={styles.legend}>
          <Legend items={colored.map(({ name, color }) => ({ name, color }))} />
        </div>
      )}
      <LineChart series={colored} xLabels={XL24} height={height} />
    </>
  )
}

export function CpuByClusterCard() {
  const { filters } = useFilters()
  const data = useOverviewMetrics(filters).data
  const clusters = data?.cpu_by_cluster ?? []
  return (
    <MetricPanel
      title="CPU by cluster, 24 h"
      right="Container Insights"
      stale={data?.stale}
      notice={<RegionErrors data={data} />}
      empty={!data || hasNoInsights(data, clusters)}
      emptyText={data ? INSIGHTS_OFF_TEXT : 'No data yet'}
    >
      {data && <ClusterLines data={data} clusters={clusters} height={214} withLegend />}
    </MetricPanel>
  )
}

export function MemoryByClusterCard() {
  const { filters } = useFilters()
  const data = useOverviewMetrics(filters).data
  const clusters = data?.mem_by_cluster ?? []
  return (
    <MetricPanel
      title="Memory by cluster, 24 h"
      stale={data?.stale}
      notice={<RegionErrors data={data} />}
      empty={!data || hasNoInsights(data, clusters)}
      emptyText={data ? INSIGHTS_OFF_TEXT : 'No data yet'}
    >
      {data && <ClusterLines data={data} clusters={clusters} height={110} withLegend={false} />}
    </MetricPanel>
  )
}
