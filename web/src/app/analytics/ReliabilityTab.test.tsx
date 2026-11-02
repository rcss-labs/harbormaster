import { screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ANALYTICS_RELIABILITY, ANALYTICS_RELIABILITY_DAILY } from '../../test/fixtures'
import { renderWithProviders } from '../../test/setup'
import { ReliabilityTab } from './ReliabilityTab'

const NINETY_DAY_PANELS = ['Availability, last 90 days', 'Time below desired, last 90 days', 'Services getting less available']

function renderTab(data = ANALYTICS_RELIABILITY_DAILY) {
  return renderWithProviders(<ReliabilityTab data={data} showSources showProposed />)
}

describe('ReliabilityTab proposed panels', () => {
  it('renders 90-day panels without proposed styling when available', () => {
    renderTab()
    for (const name of NINETY_DAY_PANELS) {
      const panel = screen.getByRole('region', { name: new RegExp(name) })
      expect(panel).toHaveAttribute('data-variant', 'default')
      expect(within(panel).queryByText('PROPOSED')).not.toBeInTheDocument()
      expect(within(panel).queryByText(/needs new data/)).not.toBeInTheDocument()
      expect(within(panel).getByText('source service_availability_daily · kept 8760h')).toBeInTheDocument()
    }
    expect(screen.queryByText('Not collected yet')).not.toBeInTheDocument()
  })

  it('says when the daily data started when it covers only part of the 90 days', () => {
    renderTab()
    // The fixture rollup began 45 days ago, inside the 90-day window of the charts and the 60-day comparison.
    for (const name of NINETY_DAY_PANELS) {
      expect(within(screen.getByRole('region', { name: new RegExp(name) })).getByText(/^Daily data started on /)).toBeInTheDocument()
    }
  })

  it('shows the getting-worse table with fractions as percentages and the change in points', () => {
    renderTab()
    const table = within(screen.getByRole('region', { name: /Services getting less available/ }))
    const row = table.getByRole('row', { name: /payments-api/ })
    expect(within(row).getByText('99.91%')).toBeInTheDocument()
    expect(within(row).getByText('98.74%')).toBeInTheDocument()
    expect(within(row).getByText('−1.17 pts')).toBeInTheDocument()
  })

  it('keeps the dashed badge and the needs footer while the rollup is empty', () => {
    renderTab(ANALYTICS_RELIABILITY)
    const panel = screen.getByRole('region', { name: /Services getting less available/ })
    expect(panel).toHaveAttribute('data-variant', 'proposed')
    expect(within(panel).getByText('PROPOSED')).toBeInTheDocument()
    expect(within(panel).getByText(/^needs new data service_availability_daily/)).toBeInTheDocument()
  })
})

describe('ReliabilityTab stop reasons', () => {
  it('colours each API stop-reason group by its real name and greys the expected ones', () => {
    const base = ANALYTICS_RELIABILITY_DAILY.stops_by_reason
    const line = (key: string, expected = false) => ({ key, label: key, values: base.series[0].values.map(() => 1), expected })
    renderTab({
      ...ANALYTICS_RELIABILITY_DAILY,
      stops_by_reason: {
        ...base,
        series: [line('Failed load balancer health checks'), line('Out of memory (exit 137)'), line('Container exited with an error'), line('Scaled in', true)],
      },
    })
    const legend = within(screen.getByRole('region', { name: /Task stops by reason/ }))
    const swatch = (label: string) => (legend.getByText(label).previousElementSibling as HTMLElement).style.background
    expect(swatch('Failed load balancer health checks')).toBe('var(--warning)')
    expect(swatch('Out of memory (exit 137)')).toBe('var(--series-2)')
    expect(swatch('Container exited with an error')).toBe('var(--danger)')
    expect(swatch('Scaled in')).toBe('var(--muted)')
  })
})
