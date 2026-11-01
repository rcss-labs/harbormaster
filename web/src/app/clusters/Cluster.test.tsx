import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import { CLUSTER_CORE } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

beforeEach(() => {
  localStorage.clear()
})

/** The tile whose label (k) text matches; the label is a div, so the nav links are not matched. */
const tileLabelled = async (label: string): Promise<HTMLElement> => {
  const labelElement = await screen.findByText(label, { selector: 'div' })
  return labelElement.parentElement as HTMLElement
}

describe('Cluster', () => {
  it('tiles and service cards from fixture', async () => {
    renderApp(`/clusters/${CLUSTER_CORE.id}`)

    const servicesTile = await tileLabelled('Services')
    expect(within(servicesTile).getByText(String(CLUSTER_CORE.services_detail.length), { exact: true })).toBeInTheDocument()
    expect(within(servicesTile).getByText('3 steady')).toBeInTheDocument()

    const tasksTile = await tileLabelled('Tasks, 72 h')
    expect(within(tasksTile).getByText(String(CLUSTER_CORE.running), { exact: true })).toBeInTheDocument()
    expect(within(tasksTile).getByText(`of ${CLUSTER_CORE.desired} desired`)).toBeInTheDocument()

    const serviceCards = screen.getAllByRole('article')
    expect(serviceCards).toHaveLength(CLUSTER_CORE.services_detail.length)
    const notifications = within(screen.getByRole('article', { name: 'notifications' }))
    expect(notifications.getByText('2 / 2')).toBeInTheDocument()
    expect(notifications.getByText('Deploying')).toBeInTheDocument()
  })

  it('cluster header links to the VPC tree', async () => {
    renderApp(`/clusters/${CLUSTER_CORE.id}`)
    const vpcLink = await screen.findByRole('link', { name: CLUSTER_CORE.vpc!.name })
    expect(vpcLink).toHaveAttribute('href', `/networks/${CLUSTER_CORE.account_id}/${CLUSTER_CORE.region}/${CLUSTER_CORE.vpc!.vpc_id}`)
  })

  it('omits the VPC link when the cluster has no VPC', async () => {
    server.use(http.get('/api/clusters/:id', () => HttpResponse.json({ ...CLUSTER_CORE, vpc: null })))
    renderApp(`/clusters/${CLUSTER_CORE.id}`)
    await screen.findByRole('heading', { name: CLUSTER_CORE.name, level: 1 })
    expect(screen.queryByRole('link', { name: CLUSTER_CORE.vpc!.name })).not.toBeInTheDocument()
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
    const card = await screen.findByRole('article', { name: 'payments-api' })
    fireEvent.click(within(card).getByRole('button', { name: paymentsApi.tag.name }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Tag for payments-api')).toBeInTheDocument()
    fireEvent.click(within(dialog).getByRole('radio', { name: 'Staging' }))
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save tag' }))
    await waitFor(() => expect(puts).toEqual([{ url: `/api/tag-assignments/services/${paymentsApi.id}`, body: { tag_id: 2 } }]))
  })
})
