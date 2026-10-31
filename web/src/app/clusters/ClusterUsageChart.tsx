import type { ClusterMetricsDTO } from '../../api/types'
import { Banner } from '../../components/Banner'
import { Card } from '../../components/Card'
import { Legend } from '../../charts/Legend'
import { LineChart } from '../../charts/LineChart'
import { Sparkline } from '../../charts/Sparkline'
import { Tile } from '../../components/Tile'
import { XL24 } from '../../charts/format'
import { MetricPanel } from '../observe/MetricPanel'
import { percentText, presentValues } from './ServiceCardMetrics'

const DAY_MS = 24 * 60 * 60 * 1000
const CPU_COLOR = 'var(--series-1)'
const MEMORY_COLOR = 'var(--series-2)'
const DEPLOY_COLOR = 'var(--info)'

/**
 * Deployment markers on the 24 h chart. Only deploys are shown (restarts are not in the legend).
 * `at` is the fraction of the 24 h window from the first timestamp; the label is `<service first segment>:<rev>`.
 */
export function deployMarks(metrics: ClusterMetricsDTO) {
  const firstTimestamp = metrics.timestamps[0]
  const windowStart = firstTimestamp ? Date.parse(firstTimestamp) : Date.now() - DAY_MS
  return metrics.markers
    .filter((marker) => marker.kind === 'deploy')
    .map((marker) => ({
      at: (Date.parse(marker.at) - windowStart) / DAY_MS,
      label: `${marker.service ? marker.service.split('-')[0] : ''}${marker.label}`,
    }))
    .filter((mark) => mark.at >= 0 && mark.at <= 1)
}

interface ClusterUsageChartProps {
  metrics?: ClusterMetricsDTO
  insights: boolean
}

/** Cluster CPU and memory over 24 h with deployment markers (mockup cluster page, `g-wide` chart card). */
export function ClusterUsageChart({ metrics, insights }: ClusterUsageChartProps) {
  if (!insights) {
    return (
      <Card title="Cluster CPU and memory">
        <Banner kind="warn">Container Insights is off for this cluster.</Banner>
      </Card>
    )
  }

  const cpu = metrics?.cluster?.cpu ?? []
  const memory = metrics?.cluster?.mem ?? []

  return (
    <MetricPanel
      title="Cluster CPU and memory, 24 h"
      stale={metrics?.stale}
      empty={!metrics?.cluster}
    >
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
      <Sparkline values={presentValues(metrics?.cluster?.cpu ?? [])} color={CPU_COLOR} height={24} fluid />
    </Tile>
  )
}
