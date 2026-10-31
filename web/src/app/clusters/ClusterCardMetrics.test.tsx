import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { CLUSTERS_METRICS } from '../../test/fixtures'
import { ClusterCardMetrics } from './ClusterCardMetrics'

const EU_APPS_ID = Object.keys(CLUSTERS_METRICS.items).find((id) => CLUSTERS_METRICS.items[id].insights === 'enabled') ?? ''

describe('ClusterCardMetrics', () => {
  it('renders sparklines and percentages when Insights is on', () => {
    const { container } = render(
      <ClusterCardMetrics clusterId={EU_APPS_ID} insights metrics={CLUSTERS_METRICS.items[EU_APPS_ID]} />,
    )
    expect(screen.getByText('CPU, 24 h')).toBeInTheDocument()
    expect(screen.getByText('49%')).toBeInTheDocument()
    expect(screen.getByText('Memory, 24 h')).toBeInTheDocument()
    expect(screen.getByText('73%')).toBeInTheDocument()
    expect(container.querySelectorAll('svg')).toHaveLength(2)
  })

  it('renders the Insights-off banner', () => {
    const { container } = render(<ClusterCardMetrics clusterId="edge" insights={false} />)
    expect(screen.getByText('Container Insights is off, so there are no CPU and memory graphs.')).toBeInTheDocument()
    expect(container.querySelector('svg')).toBeNull()
  })

  it('renders dashes while metrics are loading', () => {
    const { container } = render(<ClusterCardMetrics clusterId={EU_APPS_ID} insights />)
    expect(screen.getByText('CPU, 24 h')).toBeInTheDocument()
    expect(screen.getByText('Memory, 24 h')).toBeInTheDocument()
    expect(screen.getAllByText('–')).toHaveLength(2)
    expect(container.querySelector('svg')).toBeNull()
  })
})
