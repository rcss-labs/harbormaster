import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import { ANALYTICS_CAPACITY, ANALYTICS_DELIVERY, ANALYTICS_OPERATIONS, ANALYTICS_RELIABILITY } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})

const FIXTURES = { delivery: ANALYTICS_DELIVERY, reliability: ANALYTICS_RELIABILITY, capacity: ANALYTICS_CAPACITY, operations: ANALYTICS_OPERATIONS }

/** Records every analytics request URL while serving the same fixtures as the default handler. */
function recordRequests() {
  const urls: URL[] = []
  server.use(
    http.get('/api/analytics/:tab', ({ params, request }) => {
      const url = new URL(request.url)
      urls.push(url)
      const fixture = FIXTURES[params.tab as keyof typeof FIXTURES]
      return HttpResponse.json({ ...fixture, window: { ...fixture.window, requested: url.searchParams.get('range') ?? '30d' } })
    }),
  )
  return urls
}

const pathname = () => screen.getByTestId('location')
const search = () => screen.getByTestId('search')
const kpis = async (name: string) => within(await screen.findByRole('list', { name }))

describe('Analytics routing', () => {
  it('/analytics redirects to /analytics/delivery and keeps the query', async () => {
    renderApp('/analytics?range=7d')
    await waitFor(() => expect(pathname()).toHaveTextContent('/analytics/delivery'))
    expect(search()).toHaveTextContent('range=7d')
  })

  it('an unknown tab redirects to delivery', async () => {
    renderApp('/analytics/nope')
    await waitFor(() => expect(pathname()).toHaveTextContent('/analytics/delivery'))
    expect(await screen.findByRole('heading', { level: 1, name: 'Analytics' })).toBeInTheDocument()
  })

  it('tab links keep ?account&tag&range and show the data window', async () => {
    renderApp('/analytics/delivery?account=482109375512&tag=3&range=7d')
    const link = await screen.findByRole('link', { name: /^Reliability/ })
    const href = new URL(link.getAttribute('href') ?? '', 'http://x')
    expect(href.pathname).toBe('/analytics/reliability')
    expect(href.searchParams.get('account')).toBe('482109375512')
    expect(href.searchParams.getAll('tag')).toEqual(['3'])
    expect(href.searchParams.get('range')).toBe('7d')
    expect(screen.getByRole('link', { name: /^Delivery/ })).toHaveTextContent('90d')
    expect(link).toHaveTextContent('7d')
    expect(screen.getByRole('link', { name: /^Capacity/ })).toHaveTextContent('live + 7d')
    expect(screen.getByRole('link', { name: /^Operations/ })).toHaveTextContent('90d')
  })
})

describe('Analytics filters', () => {
  it('the range segment updates ?range and refetches with the param', async () => {
    const urls = recordRequests()
    renderApp('/analytics/delivery')
    const user = userEvent.setup()
    const group = await screen.findByRole('radiogroup', { name: 'Range' })
    await waitFor(() => expect(urls.length).toBeGreaterThan(0))
    expect(urls[0].searchParams.get('range')).toBe('30d')
    await user.click(within(group).getByRole('radio', { name: '7d' }))
    await waitFor(() => expect(search()).toHaveTextContent('range=7d'))
    await waitFor(() => expect(urls.some((url) => url.searchParams.get('range') === '7d')).toBe(true))
  })

  it('the tag segment sets the global tag and All clears it', async () => {
    const urls = recordRequests()
    renderApp('/analytics/delivery')
    const user = userEvent.setup()
    const group = await screen.findByRole('radiogroup', { name: 'Tag' })
    expect(within(group).getAllByRole('radio').map((radio) => radio.textContent)).toEqual(['All', 'Dev', 'Staging', 'Prod'])
    expect(within(group).getByRole('radio', { name: 'All' })).toBeChecked()
    await user.click(within(group).getByRole('radio', { name: 'Prod' }))
    await waitFor(() => expect(search()).toHaveTextContent('tag=3'))
    expect(within(group).getByRole('radio', { name: 'Prod' })).toBeChecked()
    await waitFor(() => expect(urls.some((url) => url.searchParams.getAll('tag').join() === '3')).toBe(true))
    await user.click(within(group).getByRole('radio', { name: 'All' }))
    await waitFor(() => expect(search()).not.toHaveTextContent('tag='))
  })
})

describe('Analytics notices', () => {
  it('reliability shows the clamped notice from the window', async () => {
    renderApp('/analytics/reliability?range=90d')
    expect(await screen.findByText(/Showing the last 7 days\. Stopped tasks and task-count history are kept 7 days\./)).toBeInTheDocument()
  })

  it('the clamped notice names the retention for what is kept and the used window for what is shown', async () => {
    server.use(
      http.get('/api/analytics/reliability', () =>
        HttpResponse.json({ ...ANALYTICS_RELIABILITY, window: { ...ANALYTICS_RELIABILITY.window, used: '7d', clamped: true, retention: '240h' } }),
      ),
    )
    renderApp('/analytics/reliability?range=90d')
    expect(await screen.findByText(/Showing the last 7 days\. Stopped tasks and task-count history are kept 10 days\./)).toBeInTheDocument()
  })

  it('delivery has no notice when the window is not clamped', async () => {
    renderApp('/analytics/delivery')
    await screen.findByRole('list', { name: 'Delivery key figures' })
    expect(screen.queryByText(/Showing the last/)).not.toBeInTheDocument()
  })

  it('capacity dims the range filter, always shows the fixed notice and explains a click with a toast', async () => {
    renderApp('/analytics/capacity?range=24h')
    const user = userEvent.setup()
    const group = await screen.findByRole('radiogroup', { name: 'Range' })
    const radios = within(group).getAllByRole('radio')
    for (const radio of radios) {
      expect(radio).toHaveAttribute('aria-disabled', 'true')
      expect(radio).not.toBeDisabled() // stays focusable so the explanatory toast still works
    }
    expect(within(screen.getByRole('radiogroup', { name: 'Tag' })).getAllByRole('radio')[0]).not.toHaveAttribute('aria-disabled')
    expect(await screen.findByText(/Breakdowns use the latest poll/)).toBeInTheDocument()
    await user.click(within(group).getByRole('radio', { name: '90d' }))
    expect(await screen.findByText('This page does not use the range filter')).toBeInTheDocument()
    expect(search()).toHaveTextContent('range=24h')
  })
})

describe('Analytics panels', () => {
  it('shows every analytics panel as a normal panel with a factual empty state', async () => {
    renderApp('/analytics/delivery')
    const panel = (await screen.findByRole('heading', { name: 'Why rollouts failed' })).closest('section') as HTMLElement
    expect(panel).not.toHaveAttribute('data-variant')
    expect(within(panel).getByText('No failed rollouts with a recorded reason in this range')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: /Recent failed rollouts/ })).toBeInTheDocument()
    expect(document.body).not.toHaveTextContent(/needs new data|Not collected yet|existing tables only|dashed/)
  })

  it('the Display menu only has the data sources toggle', async () => {
    renderApp('/analytics/delivery')
    const user = userEvent.setup()
    await screen.findByRole('heading', { name: 'Rollouts' })
    await user.click(screen.getByRole('button', { name: 'Display' }))
    expect(screen.getAllByRole('checkbox').map((box) => box.getAttribute('aria-label') ?? box.parentElement?.textContent)).toEqual(['Show data sources'])
  })

  it('an available daily panel renders its data like any other', async () => {
    renderApp('/analytics/reliability')
    const panel = (await screen.findByRole('heading', { name: /Time below desired, last 90 days/ })).closest('section') as HTMLElement
    expect(within(panel).getByRole('group', { name: /Minutes below desired/ })).toBeInTheDocument()
  })

  it('Data used on this page lists the sources, and the Display toggle hides it', async () => {
    renderApp('/analytics/reliability')
    const user = userEvent.setup()
    const table = await screen.findByRole('table', { name: 'Data used on this page' })
    for (const source of ANALYTICS_RELIABILITY.sources) expect(within(table).getByText(source.table)).toBeInTheDocument()
    expect(within(table).getByText('HM_RETENTION_COUNTS_HISTORY')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Display' }))
    await user.click(screen.getByRole('checkbox', { name: 'Show data sources' }))
    expect(screen.queryByRole('table', { name: 'Data used on this page' })).not.toBeInTheDocument()
    expect(screen.queryByText(/^source service_counts_history/)).not.toBeInTheDocument()
  })

  it('tables show 8 rows with the total and Show all reveals the rest', async () => {
    const rows = Array.from({ length: 10 }, (_, i) => ({
      ...ANALYTICS_DELIVERY.services.rows[0],
      service_id: `svc-${i}`,
      service: `service-${i}`,
      failure_rate: 0.5 - i * 0.01,
    }))
    server.use(http.get('/api/analytics/delivery', () => HttpResponse.json({ ...ANALYTICS_DELIVERY, services: { rows, total: 30 } })))
    renderApp('/analytics/delivery')
    const user = userEvent.setup()
    const table = await screen.findByRole('table', { name: 'Services by change failure rate' })
    expect(within(table).getAllByRole('link')).toHaveLength(8)
    expect(screen.getByText(/Showing 8 of 30 services/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Show all' }))
    expect(within(table).getAllByRole('link')).toHaveLength(10)
  })

  it('shows the API error when the tab cannot be loaded', async () => {
    server.use(http.get('/api/analytics/delivery', () => HttpResponse.json({ error: { code: 'internal', message: 'Analytics are down.' } }, { status: 500 })))
    renderApp('/analytics/delivery')
    expect(await screen.findByText('Analytics are down.')).toBeInTheDocument()
  })
})

describe('Analytics KPI strips', () => {
  const expectKpis = (strip: ReturnType<typeof within>, expected: Record<string, string>) => {
    const items = strip.getAllByRole('listitem')
    expect(items).toHaveLength(Object.keys(expected).length)
    Object.entries(expected).forEach(([label, value], i) => {
      expect(items[i]).toHaveTextContent(label)
      expect(items[i]).toHaveTextContent(value)
    })
  }

  it('delivery', async () => {
    renderApp('/analytics/delivery')
    expectKpis(await kpis('Delivery key figures'), {
      Rollouts: '481.6 per day',
      'Change failure rate': '8.3%',
      'Median rollout': '5m 12sp90 13m',
      'Time to recover': '18m',
      Rollbacks: '2',
      Superseded: '3',
    })
  })

  it('reliability', async () => {
    renderApp('/analytics/reliability')
    expectKpis(await kpis('Reliability key figures'), {
      Availability: '99.62%',
      'Time below desired': '1h 35m',
      'Task stops': '31',
      'Unexpected stops': '6',
      'Out of memory': '1',
      'Placement failures': '2',
    })
  })

  it('capacity', async () => {
    renderApp('/analytics/capacity')
    expectKpis(await kpis('Capacity key figures'), {
      'Reserved vCPU': '14.5',
      'Reserved memory': '36 GiB',
      'Running tasks': '27',
      'Spot share': '22%',
      'At scaling max': '1',
      'Subnets low on IPs': '1',
    })
  })

  it('operations', async () => {
    renderApp('/analytics/operations')
    const strip = await kpis('Operations key figures')
    expect(screen.getByText('from the operation log')).toBeInTheDocument()
    expectKpis(strip, {
      Operations: '120by 4 people',
      'Revisions registered': '18',
      'Scale changes': '22',
      Restarts: '9',
      'Tasks stopped by hand': '5',
      'Terminal sessions': '7',
    })
  })
})

describe('Analytics tables', () => {
  it('operations lists who operates and the most restarted services', async () => {
    renderApp('/analytics/operations')
    const who = await screen.findByRole('table', { name: 'Who operates' })
    expect(within(who).getByText('pavel')).toBeInTheDocument()
    const restarted = screen.getByRole('table', { name: 'Most restarted services' })
    expect(within(restarted).getByRole('link', { name: 'payments-api' })).toHaveAttribute('href', expect.stringContaining('/services/'))
  })

  it('capacity lists clusters, services at their scaling max and low subnets', async () => {
    renderApp('/analytics/capacity')
    const clusters = await screen.findByRole('table', { name: 'Clusters by reserved vCPU' })
    expect(within(clusters).getByText('staging-web')).toBeInTheDocument()
    expect(within(screen.getByRole('table', { name: 'Services at auto-scaling max' })).getByText('6 / 6')).toBeInTheDocument()
    expect(within(screen.getByRole('table', { name: 'Subnets running low on IPs' })).getByText('private-a')).toBeInTheDocument()
  })
})
