import { fireEvent, screen, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import { CLUSTERS } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

beforeEach(() => {
  localStorage.clear()
})

describe('Clusters', () => {
  it('renders cards by default with 6 fixtures', async () => {
    renderApp('/clusters')
    const cards = await screen.findAllByRole('article')
    expect(cards).toHaveLength(CLUSTERS.items.length)
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  it('defaults to table when more than 30 clusters', async () => {
    const manyClusters = Array.from({ length: 31 }, (_, index) => ({
      ...CLUSTERS.items[index % CLUSTERS.items.length],
      id: `generated-${index}`,
      name: `generated-${index}`,
    }))
    server.use(http.get('/api/clusters', () => HttpResponse.json({ ...CLUSTERS, items: manyClusters })))
    renderApp('/clusters')
    expect(await screen.findByRole('table')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Table' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.queryAllByRole('article')).toHaveLength(0)
  })

  it('shows the insights-off banner only on the card of a cluster without Container Insights', async () => {
    renderApp('/clusters')
    const edgeProxyCard = within(await screen.findByRole('article', { name: 'edge-proxy' }))
    expect(edgeProxyCard.getByText('Container Insights is off, so there are no CPU and memory graphs.')).toBeInTheDocument()
    expect(screen.getAllByText(/Container Insights is off/)).toHaveLength(1)
  })

  it('card shows the 24 h CPU percent loaded from the default metrics handler', async () => {
    renderApp('/clusters')
    const euAppsCard = within(await screen.findByRole('article', { name: 'eu-apps' }))
    expect(await euAppsCard.findByText('49%')).toBeInTheDocument()
  })

  it('Clusters table › CPU column shows "Container Insights off" for edge-proxy', async () => {
    renderApp('/clusters')
    fireEvent.click(await screen.findByRole('button', { name: 'Table' }))
    const edgeProxyRow = within(await screen.findByRole('row', { name: /edge-proxy/ }))
    expect(edgeProxyRow.getByText('Container Insights off')).toBeInTheDocument()
  })
})
