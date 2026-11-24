import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import { CLUSTERS } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

beforeEach(() => localStorage.clear())

const map = () => screen.findByRole('region', { name: 'Estate map' })
const clusterId = (name: string) => CLUSTERS.items.find((c) => c.name === name)!.id

describe('EstateMap', () => {
  it('draws one row per account with one cell per cluster', async () => {
    renderApp('/')
    const region = await map()
    const rows = await within(region).findAllByRole('group')
    expect(rows.map((row) => row.getAttribute('aria-label'))).toEqual(['sandbox-admin', 'staging-example'])
    expect(within(rows[0]).getAllByRole('link')).toHaveLength(5)
    expect(within(rows[1]).getAllByRole('link')).toHaveLength(1)
  })

  it('labels each cell with cluster, status, tasks and region', async () => {
    renderApp('/')
    const cell = await within(await map()).findByRole('link', { name: /^background-jobs/ })
    expect(cell).toHaveAccessibleName('background-jobs, Needs attention, 2/4 tasks, us-east-1')
    expect(cell).toHaveAttribute('href', `/clusters/${clusterId('background-jobs')}`)
  })

  it('colours healthy cells with the cell-ok token and failing ones with bad', async () => {
    renderApp('/')
    const region = await map()
    const healthy = await within(region).findByRole('link', { name: /^ci-tools/ })
    const failing = within(region).getByRole('link', { name: /^background-jobs/ })
    expect(healthy.getAttribute('style')).toContain('var(--cell-ok)')
    expect(failing.getAttribute('style')).toContain('var(--bad)')
  })

  it('names each berth and shows its running and desired tasks while the estate is small', async () => {
    renderApp('/')
    const berth = await within(await map()).findByRole('link', { name: /^background-jobs/ })
    expect(berth).toHaveTextContent('background-jobs')
    expect(berth).toHaveTextContent('2/4')
  })

  it('groups the berths of an account by region', async () => {
    renderApp('/')
    const region = await map()
    const row = await within(region).findByRole('group', { name: 'sandbox-admin' })
    const quays = within(row).getAllByRole('list').map((list) => list.getAttribute('aria-label'))
    const regions = new Set(CLUSTERS.items.filter((c) => c.account_name === 'sandbox-admin').map((c) => c.region))
    expect(new Set(quays)).toEqual(regions)
  })

  describe('in an estate of several accounts and dozens of clusters', () => {
    const healthy = CLUSTERS.items.find((c) => c.status === 'healthy')!
    const accounts = ['alpha', 'bravo', 'charlie']
    const regions = ['eu-west-1', 'us-east-1']
    /** 60 clusters: 10 per account and region; the first `failingPerRegion` of each need attention. */
    const largeEstate = (failingPerRegion: number) =>
      accounts.flatMap((account, a) =>
        regions.flatMap((region) =>
          Array.from({ length: 10 }, (_, i) => ({
            ...healthy,
            id: `${account}-${region}-${i}`,
            name: `${account}-${region}-c${i}`,
            account_id: `00000000000${a}`,
            account_name: account,
            region,
            services: [],
            ...(i < failingPerRegion ? { status: 'needs_attention' as const, running: 1, desired: 3 } : {}),
          })),
        ),
      )
    const serve = (failingPerRegion: number) =>
      server.use(http.get('/api/clusters', () => HttpResponse.json({ ...CLUSTERS, items: largeEstate(failingPerRegion) })))

    it('shows healthy clusters as plain cells and names the ones that need attention', async () => {
      serve(1)
      renderApp('/')
      const region = await map()
      const failing = await within(region).findByRole('link', { name: /^bravo-us-east-1-c0,/ })
      expect(failing).toHaveTextContent('bravo-us-east-1-c0')
      expect(failing).toHaveTextContent('1/3')
      expect(within(region).getByRole('link', { name: /^bravo-us-east-1-c5,/ })).toBeEmptyDOMElement()
      expect(within(region).getAllByRole('link')).toHaveLength(60)
    })

    it('caps the named clusters of a region so an outage does not flood the map', async () => {
      serve(10)
      renderApp('/')
      const region = await map()
      const quay = within(await within(region).findByRole('group', { name: 'alpha' })).getByRole('list', { name: 'eu-west-1' })
      const links = within(quay).getAllByRole('link')
      expect(links).toHaveLength(10)
      expect(links.filter((link) => link.textContent !== '')).toHaveLength(4)
    })

    it('keeps the region quays and says how many clusters of the account need attention', async () => {
      serve(1)
      renderApp('/')
      const row = await within(await map()).findByRole('group', { name: 'charlie' })
      expect(within(row).getAllByRole('list').map((list) => list.getAttribute('aria-label'))).toEqual(regions)
      expect(row).toHaveTextContent('20 clusters · 2 need attention')
    })
  })

  it('tells accounts with the same name apart by their id', async () => {
    const base = CLUSTERS.items[0]
    const items = ['111111111111', '222222222222'].map((id) => ({ ...base, id: `c-${id}`, account_id: id, account_name: 'production', services: [] }))
    server.use(http.get('/api/clusters', () => HttpResponse.json({ ...CLUSTERS, items })))
    renderApp('/')
    const region = await map()
    expect(await within(region).findByRole('group', { name: 'production 111111111111' })).toHaveTextContent('111111111111')
    expect(within(region).getByRole('group', { name: 'production 222222222222' })).toBeInTheDocument()
  })

  it('opens the cluster when a cell is clicked', async () => {
    const user = userEvent.setup()
    renderApp('/')
    await user.click(await within(await map()).findByRole('link', { name: /^eu-apps/ }))
    expect(await screen.findByTestId('location')).toHaveTextContent(`/clusters/${clusterId('eu-apps')}`)
  })

  it('stays one row per account with 100 clusters', async () => {
    const base = CLUSTERS.items[0]
    const items = Array.from({ length: 100 }, (_, i) => ({ ...base, id: `c-${i}`, name: `cluster-${i}`, services: [] }))
    server.use(http.get('/api/clusters', () => HttpResponse.json({ ...CLUSTERS, items })))
    renderApp('/')
    const region = await map()
    expect(await within(region).findAllByRole('link')).toHaveLength(100)
    expect(within(region).getAllByRole('group')).toHaveLength(1)
  })
})
