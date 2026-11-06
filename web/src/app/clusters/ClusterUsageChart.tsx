import type { ClusterMetricsDTO } from '../../api/types'
import { allNull } from '../../api/observe'
import { Legend } from '../../charts/Legend'
import { LineChart } from '../../charts/LineChart'
import { XL24 } from '../../charts/format'
import { Banner } from '../../components/Banner'
import { EmptyState } from '../../components/EmptyState'
import { Panel } from '../../components/Panel'
import { StaleNote } from '../../components/StaleNote'
import styles from './clusters.module.css'

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

/** Cluster CPU and memory over 24 h with deployment markers. */
export function ClusterUsageChart({ metrics, insights, error }: ClusterUsageChartProps) {
  if (!insights) {
    return (
      <Panel title="Cluster CPU and memory">
        <Banner kind="warn">Container Insights is off for this cluster.</Banner>
      </Panel>
    )
  }

  if (!metrics && error) {
    const message = error instanceof Error ? error.message : 'The metrics could not be loaded.'
    return (
      <Panel title="Cluster CPU and memory, 24 h">
        <Banner kind="bad">{message}</Banner>
      </Panel>
    )
  }

  const cpu = metrics?.cluster?.cpu ?? []
  const memory = metrics?.cluster?.mem ?? []
  // Empty until metrics load (no "No data yet" while loading); then empty when there is no cluster series or every value is null.
  const hasNoData = metrics !== undefined && (!metrics.cluster || allNull(metrics.cluster.cpu, metrics.cluster.mem))

  return (
    <Panel title="Cluster CPU and memory, 24 h" actions={<span>Container Insights</span>}>
      {metrics?.stale && <StaleNote stale={metrics.stale} />}
      {hasNoData ? (
        <EmptyState>No data yet</EmptyState>
      ) : (
        <>
          <Legend
            items={[
              { name: 'CPU', color: CPU_COLOR },
              { name: 'Memory', color: MEMORY_COLOR },
              { name: 'Deployment', color: DEPLOY_COLOR },
            ]}
          />
          {/* The deployment label at the right edge would otherwise widen the whole page; the frame keeps it inside the panel. */}
          <div className={styles.chartFrame}>
            <LineChart
              series={[
                { name: 'CPU', color: CPU_COLOR, values: cpu },
                { name: 'Memory', color: MEMORY_COLOR, values: memory },
              ]}
              xLabels={XL24}
              height={150}
              marks={metrics ? deployMarks(metrics) : []}
            />
          </div>
        </>
      )}
    </Panel>
  )
}
