import { screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { Proposed } from '../../api/analytics'
import { ANALYTICS_RELIABILITY } from '../../test/fixtures'
import { renderWithProviders } from '../../test/setup'
import { AnalyticsPanel } from './AnalyticsPanel'

const WINDOW = { ...ANALYTICS_RELIABILITY.window, from: '2026-10-01T00:00:00Z' }

const unavailable: Proposed<unknown> = {
  available: false,
  data_since: null,
  needs: { table: 'service_availability_daily', size: '~35 MB a year', how: 'A nightly rollup.' },
  data: null,
}

function panel(props: Partial<Parameters<typeof AnalyticsPanel>[0]> = {}) {
  return renderWithProviders(
    <AnalyticsPanel title="Availability" showSources showProposed window={WINDOW} {...props}>
      <div>chart body</div>
    </AnalyticsPanel>,
  )
}

describe('AnalyticsPanel', () => {
  it('source footer text', () => {
    panel({ source: { table: 'service_counts_history', kept: '7 days' } })
    expect(screen.getByText('source service_counts_history · kept 7 days')).toBeInTheDocument()
  })

  it('hides footer when showSources is false', () => {
    panel({ source: { table: 'service_counts_history', kept: '7 days' }, showSources: false })
    expect(screen.queryByText(/source service_counts_history/)).not.toBeInTheDocument()
    expect(screen.getByText('chart body')).toBeInTheDocument()
  })

  it('proposed footer text uses needs', () => {
    panel({ proposed: unavailable })
    expect(screen.getByText('needs new data service_availability_daily · ~35 MB a year · A nightly rollup.')).toBeInTheDocument()
    expect(screen.getByText('PROPOSED')).toBeInTheDocument()
    expect(screen.getByRole('region', { name: /Availability/ })).toHaveAttribute('data-variant', 'proposed')
  })

  it('renders nothing for a proposed panel when showProposed is off', () => {
    panel({ proposed: unavailable, showProposed: false })
    expect(screen.queryByRole('region')).not.toBeInTheDocument()
    expect(screen.queryByText('chart body')).not.toBeInTheDocument()
  })

  it('dims the chart for an unavailable proposed panel and passes dim to a render-prop child', () => {
    renderWithProviders(
      <AnalyticsPanel title="Why" showSources showProposed window={WINDOW} proposed={unavailable}>
        {(dim) => <div>{dim ? 'dimmed' : 'bright'}</div>}
      </AnalyticsPanel>,
    )
    expect(screen.getByText('dimmed')).toBeInTheDocument()
  })

  it("shows 'Daily data started on' empty state when available but data_since after window", () => {
    panel({ proposed: { available: true, data_since: '2026-10-20T00:00:00Z', needs: null, data: {} } })
    expect(screen.getByText(/Daily data started on Oct 20/)).toBeInTheDocument()
    expect(screen.getByText('chart body')).toBeInTheDocument()
    expect(screen.queryByText('PROPOSED')).not.toBeInTheDocument()
    expect(screen.getByRole('region', { name: /Availability/ })).toHaveAttribute('data-variant', 'default')
  })

  it('has no empty state when data covers the window', () => {
    panel({ proposed: { available: true, data_since: '2026-09-01T00:00:00Z', needs: null, data: {} } })
    expect(screen.queryByText(/Daily data started on/)).not.toBeInTheDocument()
  })

  it('renders legend items as label and value', () => {
    panel({ legend: [{ label: 'average', color: 'green', value: '99.9%' }] })
    expect(screen.getByText('average')).toBeInTheDocument()
    expect(screen.getByText('99.9%')).toBeInTheDocument()
  })
})
