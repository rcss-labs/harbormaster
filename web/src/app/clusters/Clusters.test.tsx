import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { CLUSTERS } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

beforeEach(() => {
  localStorage.clear()
})

// Let requests still in flight from an unmounted app finish, so they cannot reach the next test's handlers.
afterEach(async () => {
  await new Promise((resolve) => setTimeout(resolve, 100))
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

  it('status and name filters narrow both views and live in the URL', async () => {
    const user = userEvent.setup()
    renderApp('/clusters')
    expect(await screen.findAllByRole('article')).toHaveLength(6)

    await user.click(screen.getByRole('button', { name: 'Needs attention' }))
    expect(await screen.findAllByRole('article')).toHaveLength(1)
    expect(screen.getByTestId('search').textContent).toContain('status=needs_attention')

    await user.type(screen.getByRole('searchbox', { name: 'Cluster name' }), 'eu')
    await waitFor(() => expect(screen.queryAllByRole('article')).toHaveLength(0))
    expect(screen.getByText('No clusters match these filters.')).toBeInTheDocument()

    // The bar and the empty state each carry a Clear filters button.
    await user.click(screen.getAllByRole('button', { name: 'Clear filters' })[0])
    expect(await screen.findAllByRole('article')).toHaveLength(6)
    expect(screen.getByTestId('search').textContent).not.toMatch(/status=|q=/)
  })

  it('the table honours the status filter from the URL', async () => {
    const user = userEvent.setup()
    renderApp('/clusters?status=healthy')
    await user.click(await screen.findByRole('button', { name: 'Table' }))
    expect(await screen.findAllByRole('row')).toHaveLength(4)
  })

  it("the bar's account select is the top bar's", async () => {
    const user = userEvent.setup()
    renderApp('/clusters')
    await screen.findAllByRole('article')
    const bar = screen.getByRole('combobox', { name: 'Account' })
    const staging = within(bar).getByRole('option', { name: /staging/i }) as HTMLOptionElement
    await user.selectOptions(bar, staging.value)
    await waitFor(() => expect(screen.getByRole('combobox', { name: 'Account filter' })).toHaveValue(staging.value))
    expect(screen.getByTestId('search').textContent).toContain('account=')
  })

  it('renders from ?status=&q= on load', async () => {
    renderApp('/clusters?status=healthy&q=stag')
    const cards = await screen.findAllByRole('article')
    expect(cards).toHaveLength(1)
    expect(screen.getByRole('article', { name: 'staging-web' })).toBeInTheDocument()
  })
})
