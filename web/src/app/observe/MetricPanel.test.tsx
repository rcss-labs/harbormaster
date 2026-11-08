import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { renderWithProviders } from '../../test/setup'
import { MetricPanel } from './MetricPanel'

describe('MetricPanel', () => {
  it('shows No data yet when empty', () => {
    renderWithProviders(
      <MetricPanel title="CPU" empty>
        <div>chart body</div>
      </MetricPanel>,
    )
    expect(screen.getByText('No data yet')).toBeInTheDocument()
    expect(screen.queryByText('chart body')).not.toBeInTheDocument()
  })

  it('shows the stale note with the reason', () => {
    renderWithProviders(
      <MetricPanel title="CPU" stale={{ reason: 'CloudWatch throttled the request', fetched_at: '2026-10-31T11:50:00Z' }}>
        <div>chart body</div>
      </MetricPanel>,
    )
    expect(screen.getByText(/Showing data from/)).toHaveTextContent('CloudWatch throttled the request')
    expect(screen.getByText('chart body')).toBeInTheDocument()
  })

  it('renders custom empty text', () => {
    renderWithProviders(
      <MetricPanel title="ALB" empty emptyText="No load balancer">
        <div />
      </MetricPanel>,
    )
    expect(screen.getByText('No load balancer')).toBeInTheDocument()
  })

  it('shows its own error with Retry in place of the chart, and hides the chart while loading', async () => {
    const retry = vi.fn()
    const { rerender } = renderWithProviders(
      <MetricPanel title="CPU" error={{ message: 'CPU could not be read.', onRetry: retry }}>
        <div>chart body</div>
      </MetricPanel>,
    )
    expect(screen.getByRole('alert')).toHaveTextContent('CPU could not be read.')
    expect(screen.queryByText('chart body')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(retry).toHaveBeenCalledTimes(1)
    rerender(
      <MetricPanel title="CPU" loading>
        <div>chart body</div>
      </MetricPanel>,
    )
    expect(screen.queryByText('chart body')).not.toBeInTheDocument()
  })
})
