import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import { OVERVIEW } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

beforeEach(() => localStorage.clear())

const strip = () => screen.findByRole('list', { name: 'Estate key figures' })
const cell = async (label: string): Promise<HTMLElement> => {
  const items = within(await strip()).getAllByRole('listitem')
  return items.find((item) => item.textContent?.startsWith(label)) as HTMLElement
}

describe('Overview KPI strip', () => {
  it('has six cells: clusters, services, tasks, average CPU, average memory and error stops', async () => {
    renderApp('/')
    const items = within(await strip()).getAllByRole('listitem')
    expect(items.map((item) => item.querySelector('span')?.textContent)).toEqual([
      'Clusters',
      'Services',
      'Tasks',
      'Avg CPU',
      'Avg memory',
      'Error stops, 24 h',
    ])
  })

  it('reads clusters and services from the overview', async () => {
    renderApp('/')
    const clusters = await cell('Clusters')
    expect(clusters).toHaveTextContent('6')
    expect(clusters).toHaveTextContent('3 healthy')
    const svc = await cell('Services')
    expect(svc).toHaveTextContent('15')
    expect(svc).toHaveTextContent('2 need attention')
  })

  it('reads average CPU and memory from the metrics average', async () => {
    renderApp('/')
    await waitFor(async () => expect(await cell('Avg CPU')).toHaveTextContent('45%'))
    expect(await cell('Avg memory')).toHaveTextContent('68%')
  })

  it('sums the error stops over the 24 hours', async () => {
    renderApp('/')
    const total = OVERVIEW.stops_per_hour.reduce((n, hour) => n + hour.error, 0)
    expect(await cell('Error stops, 24 h')).toHaveTextContent(String(total))
  })

  it('draws sparklines only where real history exists', async () => {
    renderApp('/')
    await waitFor(async () => expect((await cell('Avg CPU')).querySelector('svg')).not.toBeNull())
    expect((await cell('Tasks')).querySelector('svg')).not.toBeNull()
    expect((await cell('Error stops, 24 h')).querySelector('svg')).not.toBeNull()
    expect((await cell('Clusters')).querySelector('svg')).toBeNull()
    expect((await cell('Services')).querySelector('svg')).toBeNull()
  })

  it('shows a dash for average use when Container Insights is off everywhere', async () => {
    server.use(
      http.get('/api/overview/metrics', () =>
        HttpResponse.json({ timestamps: [], cpu_by_cluster: [], mem_by_cluster: [], avg: { cpu: null, mem: null }, stale: null, errors: [] }),
      ),
    )
    renderApp('/')
    await waitFor(async () => expect(await cell('Avg CPU')).toHaveTextContent('–'))
  })
})

describe('Overview header', () => {
  it('shows "last poll" from polled_at', async () => {
    renderApp('/')
    const meta = await screen.findByText(/last poll/)
    expect(within(meta).getByTitle(new Date(OVERVIEW.polled_at).toLocaleString())).toBeInTheDocument()
  })

  it('says "no data yet" before the first poll (zero time)', async () => {
    server.use(http.get('/api/overview', () => HttpResponse.json({ ...OVERVIEW, polled_at: '0001-01-01T00:00:00Z' })))
    renderApp('/')
    const meta = await screen.findByText(/last poll/)
    expect(within(meta).getByText('no data yet')).toBeInTheDocument()
  })
})

describe('Overview layouts', () => {
  it('opens in Triage', async () => {
    renderApp('/')
    expect(await screen.findByRole('region', { name: 'Needs attention' })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Estate map' })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Task stops per hour, 24 h' })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Why tasks stopped, 3 days' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Services' })).not.toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Triage' })).toBeChecked()
  })

  it('switches to Estate and remembers the choice across visits', async () => {
    const user = userEvent.setup()
    const first = renderApp('/')
    await user.click(await screen.findByRole('radio', { name: 'Estate' }))
    expect(await screen.findByRole('region', { name: 'Services' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Needs attention' })).not.toBeInTheDocument()
    first.unmount()

    renderApp('/')
    expect(await screen.findByRole('region', { name: 'Services' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Estate' })).toBeChecked()
    // Keep the KPI strip in both layouts.
    expect(screen.getByRole('list', { name: 'Estate key figures' })).toBeInTheDocument()
  })

  it('switches back to Triage', async () => {
    const user = userEvent.setup()
    localStorage.setItem('harbormaster-overview-layout', 'estate')
    renderApp('/')
    await user.click(await screen.findByRole('radio', { name: 'Triage' }))
    expect(await screen.findByRole('region', { name: 'Needs attention' })).toBeInTheDocument()
  })

  it('ignores an unknown stored layout', async () => {
    localStorage.setItem('harbormaster-overview-layout', 'wide')
    renderApp('/')
    expect(await screen.findByRole('region', { name: 'Needs attention' })).toBeInTheDocument()
  })

  it('shows the error message when the overview cannot be loaded', async () => {
    server.use(http.get('/api/overview', () => HttpResponse.json({ error: 'boom' }, { status: 500 })))
    renderApp('/')
    expect(await screen.findByRole('heading', { level: 1, name: 'Overview' })).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByRole('list', { name: 'Estate key figures' })).not.toBeInTheDocument())
  })

  it('lists every stop reason in the Why tasks stopped panel', async () => {
    renderApp('/')
    const reasons = await screen.findByRole('region', { name: 'Why tasks stopped, 3 days' })
    for (const reason of OVERVIEW.stop_reasons) expect(within(reasons).getByText(reason.reason)).toBeInTheDocument()
  })

  it('KPI tiles link and agree with the lists', async () => {
    renderApp('/')
    const hrefOf = async (label: string) => (await cell(label)).querySelector('a')?.getAttribute('href')
    expect(await hrefOf('Clusters')).toBe('/clusters')
    expect(await hrefOf('Services')).toBe('/services?view=problems')
    expect(await hrefOf('Tasks')).toBe('/tasks')
    expect(await hrefOf('Error stops, 24 h')).toBe('/analytics/reliability')
    expect((await cell('Avg CPU')).querySelector('a')).toBeNull()

    const map = await screen.findByRole('region', { name: 'Estate map' })
    await waitFor(() => expect(within(map).getAllByRole('link')).toHaveLength(OVERVIEW.clusters.length))
    expect(await cell('Clusters')).toHaveTextContent(String(OVERVIEW.clusters.length))
    expect(await cell('Services')).toHaveTextContent(String(OVERVIEW.services.total))
    const reasons = await screen.findByRole('region', { name: 'Why tasks stopped, 3 days' })
    for (const reason of OVERVIEW.stop_reasons) expect(within(reasons).getByText(String(reason.count))).toBeInTheDocument()
  })

  it('two columns class present', async () => {
    renderApp('/')
    const attention = await screen.findByRole('region', { name: 'Needs attention' })
    expect(attention.closest('.hm-wide-split')).not.toBeNull()
    expect((await screen.findByRole('region', { name: 'Estate map' })).closest('.hm-wide-split')).not.toBeNull()
  })

  it('warns when a tier the page depends on is stale', async () => {
    renderApp('/')
    // The fixture poller reports the non-core tiers as healthy, so no notice is shown.
    await screen.findByRole('region', { name: 'Needs attention' })
    expect(screen.queryByText(/Some data on this page is stale/)).not.toBeInTheDocument()
  })
})
