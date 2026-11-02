import { screen, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import type { AnalyticsCapacity } from '../../api/analytics'
import { ANALYTICS_CAPACITY, ANALYTICS_CAPACITY_DAILY } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})

// CapacityTab reads the signed-in user's tags, so it is rendered through the app shell.
async function renderTab(data: AnalyticsCapacity) {
  server.use(http.get('/api/analytics/capacity', () => HttpResponse.json(data)))
  renderApp('/analytics/capacity')
  await screen.findByRole('region', { name: /Reserved vs used vCPU, last 90 days/ })
}

const DAILY_PANELS = ['Reserved vs used vCPU, last 90 days', 'Spot share, last 90 days', 'Memory reserved vs used p95', 'Most over-reserved services']

describe('CapacityTab proposed panels', () => {
  it('renders the rollup panels without proposed styling when available', async () => {
    await renderTab(ANALYTICS_CAPACITY_DAILY)
    for (const name of DAILY_PANELS) {
      const panel = screen.getByRole('region', { name: new RegExp(name) })
      expect(panel).toHaveAttribute('data-variant', 'default')
      expect(within(panel).queryByText('PROPOSED')).not.toBeInTheDocument()
      expect(within(panel).queryByText(/needs new data/)).not.toBeInTheDocument()
      expect(within(panel).getByText(/^Daily data started on /)).toBeInTheDocument()
    }
    expect(within(screen.getByRole('region', { name: /Reserved vs used vCPU/ })).getByText('source cluster_capacity_daily · kept 8760h')).toBeInTheDocument()
    expect(within(screen.getByRole('region', { name: /Most over-reserved/ })).getByText('source service_util_daily · kept 2160h')).toBeInTheDocument()
  })

  it('shows the over-reserved row with per-task sizes and unused vCPU', async () => {
    await renderTab(ANALYTICS_CAPACITY_DAILY)
    const row = within(screen.getByRole('region', { name: /Most over-reserved/ })).getByRole('row', { name: /payments-api/ })
    expect(within(row).getByText('9.0')).toBeInTheDocument()
    expect(within(row).getByText('25%')).toBeInTheDocument()
  })

  it('stays dashed with the needs footer while the rollups are empty', async () => {
    await renderTab(ANALYTICS_CAPACITY)
    for (const name of DAILY_PANELS) {
      const panel = screen.getByRole('region', { name: new RegExp(name) })
      expect(panel).toHaveAttribute('data-variant', 'proposed')
      expect(within(panel).getByText('PROPOSED')).toBeInTheDocument()
    }
  })
})

describe('CapacityTab subnets', () => {
  it('renders used_pct as a 0..1 fraction: 0.92 reads 92% with a 92% bar', async () => {
    await renderTab(ANALYTICS_CAPACITY)
    const row = screen.getByRole('row', { name: /private-a/ })
    expect(within(row).getByText('92%')).toBeInTheDocument()
    expect(row.querySelector('[data-bar]')).toHaveStyle({ width: '92%' })
  })
})

describe('CapacityTab whole-cluster labels', () => {
  it('says the cluster_capacity_daily panels total whole clusters across all tags', async () => {
    await renderTab(ANALYTICS_CAPACITY_DAILY)
    for (const name of ['Reserved vs used vCPU, last 90 days', 'Spot share, last 90 days', 'Memory reserved vs used p95']) {
      expect(within(screen.getByRole('region', { name: new RegExp(name) })).getByText(/whole cluster, all tags/)).toBeInTheDocument()
    }
    expect(within(screen.getByRole('region', { name: /Most over-reserved/ })).queryByText(/whole cluster/)).not.toBeInTheDocument()
  })
})
