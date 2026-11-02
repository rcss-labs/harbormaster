import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import type { ClusterCardDTO } from '../../api/types'
import { CLUSTERS } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

beforeEach(() => {
  localStorage.clear()
  localStorage.setItem('harbormaster-overview-layout', 'estate')
})

const services = () => screen.findByRole('region', { name: 'Services' })
const groupHeader = (region: HTMLElement, name: RegExp) => within(region).getByRole('button', { name })
const serviceLinks = (scope: HTMLElement) => within(scope).queryAllByRole('link', { name: /^[a-z0-9-]+$/ })

/** One cluster with `failing` failing services and `steady` steady ones. */
const crowdedCluster = (failing: number, steady = 0): ClusterCardDTO => {
  const base = CLUSTERS.items[0]
  const make = (n: number, status: 'failing' | 'steady') =>
    Array.from({ length: n }, (_, i) => ({ id: `${status}-${i}`, name: `${status}-svc-${String(i).padStart(2, '0')}`, status }))
  return { ...base, id: 'crowd', name: 'crowd', status: 'needs_attention', services: [...make(failing, 'failing'), ...make(steady, 'steady')] }
}
const serveClusters = (items: ClusterCardDTO[]) => server.use(http.get('/api/clusters', () => HttpResponse.json({ ...CLUSTERS, items })))

describe('Estate layout', () => {
  it('groups by cluster with Problems only on, hiding healthy clusters and saying how many', async () => {
    renderApp('/')
    const region = await services()
    await within(region).findByRole('button', { name: /core-services/ })
    expect(groupHeader(region, /background-jobs/)).toBeInTheDocument()
    expect(groupHeader(region, /eu-apps/)).toBeInTheDocument()
    expect(within(region).queryByRole('button', { name: /ci-tools/ })).not.toBeInTheDocument()
    expect(within(region).getByText('3 healthy clusters hidden')).toBeInTheDocument()
    expect(within(region).getByRole('checkbox', { name: 'Problems only' })).toBeChecked()
  })

  it('lists only problem services in a group and counts the steady ones it hid', async () => {
    renderApp('/')
    const region = await services()
    await within(region).findByRole('link', { name: 'notifications' })
    expect(within(region).queryByRole('link', { name: 'payments-api' })).not.toBeInTheDocument()
    expect(within(region).getByText('3 steady services hidden')).toBeInTheDocument()
  })

  it('shows every group and service once Problems only is switched off', async () => {
    const user = userEvent.setup()
    renderApp('/')
    const region = await services()
    await user.click(await within(region).findByRole('checkbox', { name: 'Problems only' }))
    expect(within(region).getByRole('button', { name: /ci-tools/ })).toBeInTheDocument()
    expect(within(region).getByRole('button', { name: /staging-web/ })).toBeInTheDocument()
    expect(within(region).queryByText(/healthy clusters hidden/)).not.toBeInTheDocument()
    // Problem groups open; fully healthy groups stay collapsed until opened.
    expect(within(region).getByRole('link', { name: 'payments-api' })).toBeInTheDocument()
    expect(within(region).queryByRole('link', { name: 'jenkins-controller' })).not.toBeInTheDocument()
    await user.click(groupHeader(region, /ci-tools/))
    expect(within(region).getByRole('link', { name: 'jenkins-controller' })).toBeInTheDocument()
  })

  it('"Show all" in the hidden note switches Problems only off', async () => {
    const user = userEvent.setup()
    renderApp('/')
    const region = await services()
    await user.click(await within(region).findByRole('button', { name: 'Show all' }))
    expect(within(region).getByRole('checkbox', { name: 'Problems only' })).not.toBeChecked()
  })

  it('groups by account and counts hidden healthy accounts', async () => {
    const user = userEvent.setup()
    renderApp('/')
    const region = await services()
    await user.click(await within(region).findByRole('radio', { name: 'Account' }))
    expect(groupHeader(region, /homelab-admin/)).toBeInTheDocument()
    expect(within(region).queryByRole('button', { name: /staging-example/ })).not.toBeInTheDocument()
    expect(within(region).getByText('1 healthy account hidden')).toBeInTheDocument()
    // Rows name their cluster when grouped by account.
    expect(within(region).getByText('background-jobs')).toBeInTheDocument()
  })

  it('collapses and expands a group', async () => {
    const user = userEvent.setup()
    renderApp('/')
    const region = await services()
    const header = await within(region).findByRole('button', { name: /core-services/ })
    expect(header).toHaveAttribute('aria-expanded', 'true')
    await user.click(header)
    expect(header).toHaveAttribute('aria-expanded', 'false')
    expect(within(region).queryByRole('link', { name: 'notifications' })).not.toBeInTheDocument()
    await user.click(header)
    expect(within(region).getByRole('link', { name: 'notifications' })).toBeInTheDocument()
  })

  it('filters by service or cluster text', async () => {
    const user = userEvent.setup()
    renderApp('/')
    const region = await services()
    await user.type(await within(region).findByRole('textbox', { name: 'Filter services or clusters' }), 'image')
    expect(within(region).getByRole('link', { name: 'image-resizer' })).toBeInTheDocument()
    expect(within(region).queryByRole('link', { name: 'notifications' })).not.toBeInTheDocument()
    expect(within(region).queryByRole('button', { name: /eu-apps/ })).not.toBeInTheDocument()
  })

  it('says so when nothing matches', async () => {
    const user = userEvent.setup()
    renderApp('/')
    const region = await services()
    await user.type(await within(region).findByRole('textbox', { name: 'Filter services or clusters' }), 'zzz-nothing')
    expect(within(region).getByText('No services match.')).toBeInTheDocument()
  })

  it('keeps the filter and grouping in the URL', async () => {
    const user = userEvent.setup()
    renderApp('/')
    const region = await services()
    await user.click(await within(region).findByRole('radio', { name: 'Account' }))
    await user.click(within(region).getByRole('checkbox', { name: 'Problems only' }))
    await user.type(within(region).getByRole('textbox', { name: 'Filter services or clusters' }), 'api')
    const search = new URLSearchParams(screen.getByTestId('search').textContent ?? '')
    expect(search.get('group')).toBe('account')
    expect(search.get('problems')).toBe('0')
    expect(search.get('q')).toBe('api')
  })

  it('caps a group at 12 rows and says how many more there are', async () => {
    serveClusters([crowdedCluster(20)])
    renderApp('/')
    const region = await services()
    await within(region).findByRole('button', { name: /crowd/ })
    expect(serviceLinks(region)).toHaveLength(12)
    expect(within(region).getByText('8 more services · filter to narrow down')).toBeInTheDocument()
  })

  it('can reveal more rows in a group, but never more than the bound', async () => {
    serveClusters([crowdedCluster(90)])
    const user = userEvent.setup()
    renderApp('/')
    const region = await services()
    await user.click(await within(region).findByRole('button', { name: 'Show more' }))
    expect(serviceLinks(region)).toHaveLength(60)
    expect(within(region).queryByRole('button', { name: 'Show more' })).not.toBeInTheDocument()
    expect(within(region).getByText('30 more services · filter to narrow down')).toBeInTheDocument()
  })

  it('lists problems before steady services and sorts the worst first', async () => {
    serveClusters([crowdedCluster(2, 3)])
    const user = userEvent.setup()
    renderApp('/')
    const region = await services()
    await user.click(await within(region).findByRole('checkbox', { name: 'Problems only' }))
    expect(serviceLinks(region).map((link) => link.textContent)).toEqual([
      'failing-svc-00',
      'failing-svc-01',
      'steady-svc-00',
      'steady-svc-01',
      'steady-svc-02',
    ])
  })

  it('renders 25 groups at a time across 100 clusters', async () => {
    const items = Array.from({ length: 100 }, (_, i) => ({ ...crowdedCluster(20), id: `crowd-${i}`, name: `crowd-${i}` }))
    serveClusters(items)
    const user = userEvent.setup()
    renderApp('/')
    const region = await services()
    await within(region).findByText('crowd-0', undefined, { timeout: 5000 })
    expect(within(region).getAllByRole('button', { name: /^crowd-/ })).toHaveLength(25)
    expect(serviceLinks(region)).toHaveLength(25 * 12)
    await user.click(within(region).getByRole('button', { name: 'Show 25 more clusters' }))
    expect(within(region).getAllByRole('button', { name: /^crowd-/ })).toHaveLength(50)
  }, 20_000)
})
