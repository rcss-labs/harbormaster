import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { CLUSTER_METRICS_CORE } from '../../test/fixtures'
import type { ClusterMetricsDTO } from '../../api/types'
import { ClusterUsageChart } from './ClusterUsageChart'

describe('ClusterUsageChart', () => {
  it('renders markers from the metrics response', () => {
    const timestamps = CLUSTER_METRICS_CORE.timestamps
    const metrics: ClusterMetricsDTO = {
      ...CLUSTER_METRICS_CORE,
      markers: [{ at: timestamps[Math.floor(timestamps.length / 2)], label: ':42', kind: 'deploy', failed: false, service: 'payments-api' }],
    }

    render(<ClusterUsageChart metrics={metrics} insights />)

    expect(screen.getByText('Cluster CPU and memory, 24 h')).toBeInTheDocument()
    expect(screen.getByText('Deployment')).toBeInTheDocument()
    expect(screen.getByText('payments:42')).toBeInTheDocument()
  })

  it('renders No data yet when every value is null', () => {
    const allNullMetrics: ClusterMetricsDTO = {
      ...CLUSTER_METRICS_CORE,
      cluster: { cpu: [null, null], mem: [null, null] },
      now: { cpu: null, mem: null },
    }

    render(<ClusterUsageChart metrics={allNullMetrics} insights />)

    expect(screen.getByText('No data yet')).toBeInTheDocument()
  })

  it('does not say No data yet while metrics are loading', () => {
    render(<ClusterUsageChart insights />)

    expect(screen.queryByText('No data yet')).not.toBeInTheDocument()
  })

  it('Insights off shows the banner and no chart', () => {
    render(<ClusterUsageChart metrics={CLUSTER_METRICS_CORE} insights={false} />)

    expect(screen.getByText('Cluster CPU and memory')).toBeInTheDocument()
    expect(screen.getByText('Container Insights is off for this cluster.')).toBeInTheDocument()
    expect(screen.queryByText('Deployment')).not.toBeInTheDocument()
  })
})
