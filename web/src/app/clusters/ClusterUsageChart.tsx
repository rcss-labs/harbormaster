import type { ClusterMetricsDTO } from '../../api/types'
import { Banner } from '../../components/Banner'
import { Card } from '../../components/Card'
import { Legend } from '../../charts/Legend'
import { LineChart } from '../../charts/LineChart'
import { Sparkline } from '../../charts/Sparkline'
import { Tile } from '../../components/Tile'
import { XL24 } from '../../charts/format'
import { allNull } from '../../api/observe'
import { MetricPanel } from '../observe/MetricPanel'
import { percentText, presentValues } from './ServiceCardMetrics'

const DAY_MS = 24 * 60 * 60 * 1000
const CPU_COLOR = 'var(--series-1)'
const MEMORY_COLOR = 'var(--series-2)'
const DEPLOY_COLOR = 'var(--info)'

/**
 * Deployment markers on the 24 h chart. Only deploys are shown (restarts are not in the legend).
 * `at` is the fraction across the plotted series (first to last timestamp), like the chart's own x axis; the label is
 * `<service first segment>:<rev>`. The grid can end up to 40 minutes before now (the in-progress bucket is left out),
 * so a deployment newer than the last timestamp is pinned to the right edge instead of being dropped.
 */
export function deployMarks(metrics: ClusterMetricsDTO) {
  const firstTimestamp = metrics.timestamps[0]
  const lastTimestamp = metrics.timestamps[metrics.timestamps.length - 1]
  const windowStart = firstTimestamp ? Date.parse(firstTimestamp) : Date.now() - DAY_MS
  const plottedSpan = metrics.timestamps.length > 1 ? Date.parse(lastTimestamp) - windowStart : DAY_MS
  return metrics.markers
    .filter((marker) => marker.kind === 'deploy')
    .map((marker) => ({
      at: Math.min((Date.parse(marker.at) - windowStart) / plottedSpan, 1),
      label: `${marker.service ? marker.service.split('-')[0] : ''}${marker.label}`,
    }))
    .filter((mark) => mark.at >= 0)
}

interface ClusterUsageChartProps {
  metrics?: ClusterMetricsDTO
  insights: boolean
  /** The metrics query error; shown only when there is no data to fall back on. */
  error?: unknown
}

/** Cluster CPU and memory over 24 h with deployment markers (mockup cluster page, `g-wide` chart card). */
export function ClusterUsageChart({ metrics, insights, error }: ClusterUsageChartProps) {
  if (!insights) {
    return (
      <Card title="Cluster CPU and memory">
        <Banner kind="warn">Container Insights is off for this cluster.</Banner>
      </Card>
    )
  }

  if (!metrics && error) {
    const message = error instanceof Error ? error.message : 'The metrics could not be loaded.'
    return (
      <Card title="Cluster CPU and memory, 24 h">
        <Banner kind="bad">{message}</Banner>
      </Card>
    )
  }

  const cpu = metrics?.cluster?.cpu ?? []
  const memory = metrics?.cluster?.mem ?? []
  // Empty until metrics load (no "No data yet" while loading); then empty when there is no cluster series or every value is null.
  const hasNoData = metrics !== undefined && (!metrics.cluster || allNull(metrics.cluster.cpu, metrics.cluster.mem))

  return (
    <MetricPanel title="Cluster CPU and memory, 24 h" stale={metrics?.stale} empty={hasNoData}>
      <Legend
        items={[
          { name: 'CPU', color: CPU_COLOR },
          { name: 'Memory', color: MEMORY_COLOR },
          { name: 'Deployment', color: DEPLOY_COLOR },
        ]}
      />
      <LineChart
        series={[
          { name: 'CPU', color: CPU_COLOR, values: cpu },
          { name: 'Memory', color: MEMORY_COLOR, values: memory },
        ]}
        xLabels={XL24}
        height={150}
        marks={metrics ? deployMarks(metrics) : []}
      />
    </MetricPanel>
  )
}

/** The "Use now" tile: current cluster CPU and memory with a CPU sparkline. */
export function UseNowTile({ metrics, insights }: ClusterUsageChartProps) {
  if (!insights) {
    return <Tile k="Use now" v={<small>Container Insights off</small>} />
  }
  const cpuHistory = presentValues(metrics?.cluster?.cpu ?? [])
  return (
    <Tile
      k="Use now"
      v={
        <>
          {percentText(metrics?.now?.cpu)}
          <small>CPU</small> {percentText(metrics?.now?.mem)}
          <small>memory</small>
        </>
      }
    >
      {cpuHistory.length > 0 ? <Sparkline values={cpuHistory} color={CPU_COLOR} height={24} fluid /> : '–'}
    </Tile>
  )
}
