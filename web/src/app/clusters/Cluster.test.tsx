import { screen, within } from '@testing-library/react'
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
