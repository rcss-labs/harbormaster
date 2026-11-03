import { screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { RollupPanel } from '../../api/analytics'
import { ANALYTICS_RELIABILITY } from '../../test/fixtures'
import { renderWithProviders } from '../../test/setup'
import { AnalyticsPanel } from './AnalyticsPanel'

const WINDOW = { ...ANALYTICS_RELIABILITY.window, from: '2026-10-01T00:00:00Z' }

const unavailable: RollupPanel<unknown> = {
  available: false,
  data_since: null,
  data: null,
}

function panel(props: Partial<Parameters<typeof AnalyticsPanel>[0]> = {}) {
  return renderWithProviders(
    <AnalyticsPanel title="Availability" showSources window={WINDOW} {...props}>
      <div>chart body</div>
    </AnalyticsPanel>,
  )
}

describe('AnalyticsPanel', () => {
  it('keeps the sub-title out of the heading and region names', () => {
    panel({ sub: 'running ≥ desired' })
    expect(screen.getByRole('heading', { level: 2, name: 'Availability' })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Availability' })).toBeInTheDocument()
    expect(screen.getByText('running ≥ desired')).toBeInTheDocument()
  })

  it('source footer text', () => {
    panel({ source: { table: 'service_counts_history', kept: '7 days' } })
    expect(screen.getByText('source service_counts_history · kept 7 days')).toBeInTheDocument()
  })

  it('hides footer when showSources is false', () => {
    panel({ source: { table: 'service_counts_history', kept: '7 days' }, showSources: false })
    expect(screen.queryByText(/source service_counts_history/)).not.toBeInTheDocument()
    expect(screen.getByText('chart body')).toBeInTheDocument()
  })

  it('an unavailable daily panel looks like any other: source footer, no variant', () => {
    panel({ rollup: unavailable, source: { table: 'service_availability_daily', kept: '8760h' } })
    expect(screen.getByText('source service_availability_daily · kept 8760h')).toBeInTheDocument()
    expect(screen.queryByText(/needs new data/i)).not.toBeInTheDocument()
    expect(screen.getByRole('region', { name: /Availability/ })).not.toHaveAttribute('data-variant')
    expect(screen.getByText('chart body')).toBeInTheDocument()
  })

  it("shows 'Daily data started on' empty state when available but data_since after window", () => {
    panel({ rollup: { available: true, data_since: '2026-10-20T00:00:00Z', data: {} } })
    expect(screen.getByText(/Daily data started on Oct 20/)).toBeInTheDocument()
    expect(screen.getByText('chart body')).toBeInTheDocument()
  })

  it('has no empty state when data covers the window', () => {
    panel({ rollup: { available: true, data_since: '2026-09-01T00:00:00Z', data: {} } })
    expect(screen.queryByText(/Daily data started on/)).not.toBeInTheDocument()
  })

  it('renders legend items as label and value', () => {
    panel({ legend: [{ label: 'average', color: 'green', value: '99.9%' }] })
    expect(screen.getByText('average')).toBeInTheDocument()
    expect(screen.getByText('99.9%')).toBeInTheDocument()
  })
})
