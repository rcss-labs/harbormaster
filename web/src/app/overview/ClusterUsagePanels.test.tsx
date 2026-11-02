import { screen, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import type { OverviewMetricsDTO } from '../../api/types'
import { OVERVIEW_METRICS } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

beforeEach(() => localStorage.clear())

const flat = (value: number) => Array.from({ length: 24 }, () => value)
const withCpu = (series: { id: string; label: string; values: (number | null)[] }[]): OverviewMetricsDTO => ({
  ...OVERVIEW_METRICS,
  cpu_by_cluster: series,
})

const busiest = () => screen.findByRole('region', { name: 'Busiest clusters, CPU' })

describe('Busiest clusters panel', () => {
  it('lists the top 6 clusters by latest CPU, highest first, skipping "others"', async () => {
    const series = Array.from({ length: 8 }, (_, i) => ({ id: `c${i}`, label: `cluster-${i}`, values: flat(10 + i * 5) }))
    series.push({ id: 'others', label: 'others', values: flat(99) })
    server.use(http.get('/api/overview/metrics', () => HttpResponse.json(withCpu(series))))
    renderApp('/')
    const region = await busiest()
    const links = await within(region).findAllByRole('link', { name: /^cluster-/ })
    expect(links.map((link) => link.textContent)).toEqual(
      ['cluster-7', 'cluster-6', 'cluster-5', 'cluster-4', 'cluster-3', 'cluster-2'].map((name) => expect.stringContaining(name)),
    )
    expect(within(region).queryByText('others')).not.toBeInTheDocument()
    expect(links[0]).toHaveTextContent('45%')
    expect(links[0]).toHaveAttribute('href', '/clusters/c7')
  })

  it('uses the latest datapoint that exists', async () => {
    server.use(
      http.get('/api/overview/metrics', () =>
        HttpResponse.json(withCpu([{ id: 'c1', label: 'only-cluster', values: [...flat(30).slice(0, 22), 62, null] }])),
      ),
    )
    renderApp('/')
    expect(await within(await busiest()).findByRole('link', { name: /only-cluster/ })).toHaveTextContent('62%')
  })

  it('shows the Container Insights message when no cluster reports CPU', async () => {
    server.use(
      http.get('/api/overview/metrics', () =>
        HttpResponse.json({ ...OVERVIEW_METRICS, cpu_by_cluster: [], mem_by_cluster: [], avg: { cpu: null, mem: null } }),
      ),
    )
    renderApp('/')
    expect(await within(await busiest()).findByText('Container Insights is off for every cluster you can see.')).toBeInTheDocument()
  })

  it('names the regions that could not be read', async () => {
    server.use(
      http.get('/api/overview/metrics', () =>
        HttpResponse.json({
          ...OVERVIEW_METRICS,
          errors: [{ account_id: '482109375512', region: 'eu-west-1', code: 'throttled', message: 'CloudWatch is throttling requests. Try again shortly.' }],
        }),
      ),
    )
    renderApp('/')
    expect(await within(await busiest()).findByText(/CloudWatch could not be read for eu-west-1/)).toBeInTheDocument()
  })
})
