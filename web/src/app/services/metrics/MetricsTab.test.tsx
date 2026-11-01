import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import {
  SERVICE_METRICS_CATALOG_NO_INSIGHTS,
  SERVICE_METRICS_PAYMENTS,
  SERVICE_METRICS_STALE,
  SERVICE_PAYMENTS,
} from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'

const metricsPath = `/services/${SERVICE_PAYMENTS.id}/metrics`

describe('MetricsTab', () => {
  it('renders four panels for 24h and the resolution text', async () => {
    const ranges: (string | null)[] = []
    server.use(
      http.get('/api/services/:id/metrics', ({ request }) => {
        ranges.push(new URL(request.url).searchParams.get('range'))
        return HttpResponse.json({ ...SERVICE_METRICS_PAYMENTS, range: '24h', period: 300 })
      }),
    )
    renderApp(metricsPath)

    expect(await screen.findByText('CPU utilization')).toBeInTheDocument()
    expect(screen.getByText('Memory utilization')).toBeInTheDocument()
    expect(screen.getByText('Network')).toBeInTheDocument()
    expect(screen.getByText('Running task count')).toBeInTheDocument()
    expect(
      screen.getByText(`Container Insights via CloudWatch · 5-minute resolution, averaged across ${SERVICE_PAYMENTS.running} tasks`),
    ).toBeInTheDocument()
    expect(ranges).toContain('24h')
  })

  it('changing the range updates ?range= and refetches with range=6h', async () => {
    const ranges: (string | null)[] = []
    server.use(
      http.get('/api/services/:id/metrics', ({ request }) => {
        ranges.push(new URL(request.url).searchParams.get('range'))
        return HttpResponse.json(SERVICE_METRICS_PAYMENTS)
      }),
    )
    renderApp(metricsPath)
    await screen.findByText('CPU utilization')

    await userEvent.setup().click(within(screen.getByRole('group', { name: 'Range' })).getByRole('button', { name: '6h' }))

    expect(screen.getByTestId('search')).toHaveTextContent('range=6h')
    await waitFor(() => expect(ranges).toContain('6h'))
  })

  it('renders the Container Insights banner and no chart when insights is disabled', async () => {
    server.use(http.get('/api/services/:id/metrics', () => HttpResponse.json(SERVICE_METRICS_CATALOG_NO_INSIGHTS)))
    renderApp(metricsPath)

    expect(await screen.findByText(/Container Insights is off for/)).toHaveTextContent(
      `Container Insights is off for ${SERVICE_PAYMENTS.cluster.name}, so per-service metrics are not available.`,
    )
    expect(screen.queryByText('CPU utilization')).not.toBeInTheDocument()
    expect(screen.queryByRole('group', { name: 'Range' })).not.toBeInTheDocument()
  })

  it('renders No data yet when every value is null', async () => {
    const gaps = (points: number) => Array.from({ length: points }, () => null)
    server.use(
      http.get('/api/services/:id/metrics', () =>
        HttpResponse.json({
          ...SERVICE_METRICS_PAYMENTS,
          series: { cpu: gaps(12), mem: gaps(12), net_rx_kbps: gaps(12), net_tx_kbps: gaps(12), tasks: gaps(12) },
          now: { ...SERVICE_METRICS_PAYMENTS.now!, cpu: null, mem: null },
        }),
      ),
    )
    renderApp(metricsPath)

    expect(await screen.findAllByText('No data yet')).toHaveLength(4)
  })

  it('shows the API message in a banner when CloudWatch is throttled and nothing is cached', async () => {
    server.use(
      http.get('/api/services/:id/metrics', () =>
        HttpResponse.json({ error: { code: 'throttled', message: 'CloudWatch is throttling requests.' } }, { status: 429 }),
      ),
    )
    renderApp(metricsPath)

    expect(await screen.findByText('CloudWatch is throttling requests.')).toBeInTheDocument()
    expect(screen.queryByText('CPU utilization')).not.toBeInTheDocument()
  })

  it('renders the stale note', async () => {
    server.use(http.get('/api/services/:id/metrics', () => HttpResponse.json(SERVICE_METRICS_STALE)))
    renderApp(metricsPath)

    const notes = await screen.findAllByText(/Showing data from/)
    expect(notes).toHaveLength(4)
    expect(notes[0]).toHaveTextContent('CloudWatch is throttling requests')
  })
})
