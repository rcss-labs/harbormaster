import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import { CLUSTER_CORE } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'
import type { ClusterDTO, ClusterServiceDTO } from '../../api/types'

beforeEach(() => {
  localStorage.clear()
})

/** The value text of the KPI cell with this label. */
const kpi = async (label: string): Promise<HTMLElement> => {
  const strip = await screen.findByRole('list', { name: 'Cluster summary' })
  const cell = within(strip)
    .getAllByRole('listitem')
    .find((item) => item.textContent?.toUpperCase().startsWith(label.toUpperCase()))
  if (!cell) throw new Error(`No KPI ${label}`)
  return cell
}

/** Service names in the services table, in display order. */
const serviceRows = () =>
  within(screen.getByRole('table', { name: 'Services' }))
    .getAllByRole('row')
    .slice(1)
    .map((row) => within(row).getAllByRole('link')[0].textContent)

const serveCluster = (cluster: ClusterDTO) => server.use(http.get('/api/clusters/:id', () => HttpResponse.json(cluster)))

describe('Cluster', () => {
  it('header shows the account with its ID, the region, status and a VPC link', async () => {
    renderApp(`/clusters/${CLUSTER_CORE.id}`)
    const heading = await screen.findByRole('heading', { name: CLUSTER_CORE.name, level: 1 })
    const header = heading.closest('header') as HTMLElement
    expect(within(header).getByText(`${CLUSTER_CORE.account_name} (${CLUSTER_CORE.account_id})`)).toBeInTheDocument()
    expect(within(header).getByText(CLUSTER_CORE.region)).toBeInTheDocument()
    expect(within(header).getByText('Deploying')).toBeInTheDocument()
    expect(within(header).getByRole('link', { name: CLUSTER_CORE.vpc!.name })).toHaveAttribute(
      'href',
      `/networks?vpc=${CLUSTER_CORE.account_id}%2F${CLUSTER_CORE.region}%2F${CLUSTER_CORE.vpc!.vpc_id}`,
    )
  })

  it('links to the Tasks page filtered to this cluster', async () => {
    renderApp(`/clusters/${CLUSTER_CORE.id}`)
    const heading = await screen.findByRole('heading', { name: CLUSTER_CORE.name, level: 1 })
    const header = heading.closest('header') as HTMLElement
    expect(within(header).getByRole('link', { name: 'Tasks in this cluster' })).toHaveAttribute(
      'href',
      `/tasks?${new URLSearchParams({ filter: 'all', q: CLUSTER_CORE.name })}`,
    )
  })

  it('omits the VPC link when the cluster has no VPC', async () => {
    serveCluster({ ...CLUSTER_CORE, vpc: null })
    renderApp(`/clusters/${CLUSTER_CORE.id}`)
    await screen.findByRole('heading', { name: CLUSTER_CORE.name, level: 1 })
    expect(screen.queryByRole('link', { name: CLUSTER_CORE.vpc!.name })).not.toBeInTheDocument()
  })

  it('KPI strip summarises services, tasks and reserved capacity', async () => {
    renderApp(`/clusters/${CLUSTER_CORE.id}`)
    const services = await kpi('Services')
    expect(services).toHaveTextContent(String(CLUSTER_CORE.services_detail.length))
    expect(services).toHaveTextContent('3 steady')
    const tasks = await kpi('Tasks')
    expect(tasks).toHaveTextContent(`${CLUSTER_CORE.running}/${CLUSTER_CORE.desired}`)
    expect(await kpi('Reserved')).toBeInTheDocument()
    expect(await kpi('CPU now')).toBeInTheDocument()
    expect(await kpi('Memory now')).toBeInTheDocument()
  })

  it('KPI Services and Tasks link to the lists scoped to this cluster; the numbers stay', async () => {
    renderApp(`/clusters/${CLUSTER_CORE.id}`)
    const services = await kpi('Services')
    expect(within(services).getByRole('link')).toHaveAttribute('href', `/services?${new URLSearchParams({ q: CLUSTER_CORE.name })}`)
    const tasks = await kpi('Tasks')
    expect(within(tasks).getByRole('link')).toHaveAttribute('href', `/tasks?${new URLSearchParams({ filter: 'all', q: CLUSTER_CORE.name })}`)
    expect(tasks).toHaveTextContent(`${CLUSTER_CORE.running}/${CLUSTER_CORE.desired}`)
  })

  it('draws a bar per service in Tasks by service, with running and desired counts', async () => {
    renderApp(`/clusters/${CLUSTER_CORE.id}`)
    const panel = (await screen.findByRole('heading', { name: 'Tasks by service' })).closest('section') as HTMLElement
    expect(within(panel).getByRole('link', { name: 'notifications' })).toBeInTheDocument()
    expect(within(panel).getAllByText('2 / 2').length).toBeGreaterThan(0)
    expect(within(panel).getByText(/bar = running/)).toBeInTheDocument()
  })

  it('exposes each Tasks by service bar as running of desired in its accessible name', async () => {
    renderApp(`/clusters/${CLUSTER_CORE.id}`)
    const panel = (await screen.findByRole('heading', { name: 'Tasks by service' })).closest('section') as HTMLElement
    expect(within(panel).getAllByRole('img', { name: 'Running 2 of 2' }).length).toBeGreaterThan(0)
  })

  it('lists services in a table with problems first', async () => {
    renderApp(`/clusters/${CLUSTER_CORE.id}`)
    await screen.findByRole('table', { name: 'Services' })
    expect(serviceRows()[0]).toBe('notifications')
    expect(serviceRows()).toHaveLength(CLUSTER_CORE.services_detail.length)
    const row = within(screen.getByRole('row', { name: /notifications/ }))
    expect(row.getByText('Deploying')).toBeInTheDocument()
    expect(row.getByText('2 / 2')).toBeInTheDocument()
  })

  it('puts a failing service above a deploying one and pages long service lists', async () => {
    const template = CLUSTER_CORE.services_detail[0]
    const many: ClusterServiceDTO[] = Array.from({ length: 30 }, (_, index) => ({
      ...template,
      id: `svc-${index}`,
      name: `svc-${String(index).padStart(2, '0')}`,
      status: index === 29 ? 'failing' : 'steady',
    }))
    serveCluster({ ...CLUSTER_CORE, services_detail: many })
    renderApp(`/clusters/${CLUSTER_CORE.id}`)
    await screen.findByRole('table', { name: 'Services' })
    await waitFor(() => expect(serviceRows()).toHaveLength(25))
    expect(serviceRows()[0]).toBe('svc-29')
    expect(screen.getByText('1–25 of 30')).toBeInTheDocument()
  })

  it('shows No access on 404', async () => {
    server.use(
      http.get('/api/clusters/:id', () =>
        HttpResponse.json({ error: { code: 'not_found', message: 'Not found.' } }, { status: 404 }),
      ),
    )
    renderApp(`/clusters/${CLUSTER_CORE.id}`)
    expect(await screen.findByRole('heading', { name: 'No access' })).toBeInTheDocument()
    expect(screen.getByText('You have no permissions on any service in this cluster.')).toBeInTheDocument()
  })
})

describe('Cluster tag edits', () => {
  it('a service tag chip edits the service, not the cluster', async () => {
    const puts: { url: string; body: unknown }[] = []
    const paymentsApi = CLUSTER_CORE.services_detail.find((service) => service.name === 'payments-api')!
    server.use(
      http.put('/api/tag-assignments/:kind/:id', async ({ request }) => {
        puts.push({ url: new URL(request.url).pathname, body: await request.json() })
        return new HttpResponse(null, { status: 204 })
      }),
    )
    renderApp(`/clusters/${CLUSTER_CORE.id}`)
    const row = await screen.findByRole('row', { name: /payments-api/ })
    fireEvent.click(within(row).getByRole('button', { name: paymentsApi.tag.name }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Tag for payments-api')).toBeInTheDocument()
    fireEvent.click(await within(dialog).findByRole('radio', { name: 'Staging' }))
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save tag' }))
    await waitFor(() => expect(puts).toEqual([{ url: `/api/tag-assignments/services/${paymentsApi.id}`, body: { tag_id: 2 } }]))
  })

  it('the cluster tag chip opens the cluster tag dialog for admins', async () => {
    const user = userEvent.setup()
    renderApp(`/clusters/${CLUSTER_CORE.id}`)
    const heading = await screen.findByRole('heading', { name: CLUSTER_CORE.name, level: 1 })
    const header = heading.closest('header') as HTMLElement
    await user.click(await within(header).findByRole('button', { name: CLUSTER_CORE.tag.name }))
    expect(await screen.findByText(`Tag for ${CLUSTER_CORE.name}`)).toBeInTheDocument()
  })
})
