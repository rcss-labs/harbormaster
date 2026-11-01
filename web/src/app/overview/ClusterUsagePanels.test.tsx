import { screen, waitFor, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import type { OverviewMetricsDTO } from '../../api/types'
import { OVERVIEW_METRICS } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

const values = Array.from({ length: 24 }, (_, i) => 20 + i)
const eightSeries: OverviewMetricsDTO = {
  ...OVERVIEW_METRICS,
  cpu_by_cluster: Array.from({ length: 8 }, (_, i) => ({
    id: `c${i}`,
    label: i === 7 ? 'others' : `cluster-${i}`,
    values,
  })),
}

const card = async (title: string): Promise<HTMLElement> => {
  const heading = await screen.findByRole('heading', { name: title })
  return heading.closest('div')?.parentElement as HTMLElement
}

describe('ClusterUsagePanels', () => {
  it('renders one legend entry per cluster plus others', async () => {
    server.use(http.get('/api/overview/metrics', () => HttpResponse.json(eightSeries)))
    renderApp('/')
    const cpu = await card('CPU by cluster, 24 h')
    expect(await within(cpu).findByText('cluster-0')).toBeInTheDocument()
    for (let i = 0; i < 7; i++) expect(within(cpu).getByText(`cluster-${i}`)).toBeInTheDocument()
    expect(within(cpu).getByText('others')).toBeInTheDocument()
    expect(within(cpu).getByText('Container Insights')).toBeInTheDocument()
  })

  it('shows the all-off message', async () => {
    server.use(
      http.get('/api/overview/metrics', () =>
        HttpResponse.json({ ...OVERVIEW_METRICS, cpu_by_cluster: [], mem_by_cluster: [], avg: { cpu: null, mem: null } }),
      ),
    )
    renderApp('/')
    await screen.findAllByText('Container Insights is off for every cluster you can see.')
    expect(screen.getAllByText('Container Insights is off for every cluster you can see.')).toHaveLength(2)
  })

  it('shows the average tile from avg', async () => {
    renderApp('/')
    const label = await screen.findByText('Average use, all services', { selector: 'div' })
    const tile = label.parentElement as HTMLElement
    await waitFor(() => expect(tile).toHaveTextContent('45%'))
    expect(tile).toHaveTextContent('CPU')
    expect(tile).toHaveTextContent('68%')
    expect(tile).toHaveTextContent('memory')
  })

  it('names the regions that could not be read, on both charts', async () => {
    server.use(
      http.get('/api/overview/metrics', () =>
        HttpResponse.json({
          ...OVERVIEW_METRICS,
          errors: [{ account_id: '482109375512', region: 'eu-west-1', code: 'throttled', message: 'CloudWatch is throttling requests. Try again shortly.' }],
        }),
      ),
    )
    renderApp('/')
    const cpu = await card('CPU by cluster, 24 h')
    expect(await within(cpu).findByText(/eu-west-1/)).toBeInTheDocument()
    const memory = await card('Memory by cluster, 24 h')
    expect(within(memory).getByText(/eu-west-1/)).toBeInTheDocument()
  })
})
