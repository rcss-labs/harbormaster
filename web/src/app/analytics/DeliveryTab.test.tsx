import { screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ANALYTICS_DELIVERY, ANALYTICS_DELIVERY_FAILURES } from '../../test/fixtures'
import { renderWithProviders } from '../../test/setup'
import { DeliveryTab } from './DeliveryTab'

describe('DeliveryTab failure panels', () => {
  it('renders the failure panels as normal panels once reasons are recorded', () => {
    renderWithProviders(<DeliveryTab data={ANALYTICS_DELIVERY_FAILURES} showSources />)
    for (const name of [/Why rollouts failed/, /Recent failed rollouts, with reason/]) {
      const panel = screen.getByRole('region', { name })
      expect(panel).not.toHaveAttribute('data-variant')
      expect(within(panel).queryByText(/needs new data/i)).not.toBeInTheDocument()
      expect(within(panel).getByText('source deployment_history · kept 90 days')).toBeInTheDocument()
      expect(within(panel).getByText(/^Daily data started on /)).toBeInTheDocument() // reasons began 12 days into the 30-day window
    }
  })

  it('labels the stored reason groups in the chart legend and the table', () => {
    renderWithProviders(<DeliveryTab data={ANALYTICS_DELIVERY_FAILURES} showSources />)
    const chart = within(screen.getByRole('region', { name: /Why rollouts failed/ }))
    expect(chart.getByText('Circuit breaker')).toBeInTheDocument()
    expect(chart.getByText('Out of memory')).toBeInTheDocument()
    const table = within(screen.getByRole('region', { name: /Recent failed rollouts/ }))
    const first = table.getByRole('row', { name: /payments-api/ })
    expect(within(first).getByText('Circuit breaker')).toBeInTheDocument()
    expect(within(first).getByText(':41 → :42')).toBeInTheDocument()
    const second = table.getByRole('row', { name: /catalog/ })
    expect(within(second).getByText('Out of memory')).toBeInTheDocument()
    expect(within(second).getByText(':7')).toBeInTheDocument()
  })

  it('shows a factual empty state with the normal source footer before any reason is recorded', () => {
    renderWithProviders(<DeliveryTab data={ANALYTICS_DELIVERY} showSources />)
    for (const name of [/Why rollouts failed/, /Recent failed rollouts/]) {
      const panel = within(screen.getByRole('region', { name }))
      expect(panel.getByText('No failed rollouts with a recorded reason in this range')).toBeInTheDocument()
      expect(panel.getByText(/^source deployment_history · kept /)).toBeInTheDocument()
      expect(panel.queryByText(/needs new data|Not collected yet/i)).not.toBeInTheDocument()
    }
  })
})
