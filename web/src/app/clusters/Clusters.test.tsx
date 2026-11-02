import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { ACCOUNT_STAGING, CLUSTERS, TAG_STAGING } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

beforeEach(() => {
  localStorage.clear()
})

// Let requests still in flight from an unmounted app finish, so they cannot reach the next test's handlers.
afterEach(async () => {
  await new Promise((resolve) => setTimeout(resolve, 100))
})

/** Names in the first column of the body rows, in display order. */
const shownNames = () =>
  screen
    .getAllByRole('row')
    .slice(1)
    .map((row) => within(row).getAllByRole('link')[0].textContent)

const serveManyClusters = (count: number) => {
  const items = Array.from({ length: count }, (_, index) => ({
    ...CLUSTERS.items[index % CLUSTERS.items.length],
    id: `generated-${String(index).padStart(3, '0')}`,
    name: `generated-${String(index).padStart(3, '0')}`,
  }))
  server.use(http.get('/api/clusters', () => HttpResponse.json({ ...CLUSTERS, items })))
}

describe('Clusters', () => {
  it('lists clusters in a table with the worst status first', async () => {
    renderApp('/clusters')
    await screen.findByRole('table', { name: 'Clusters' })
    await waitFor(() => expect(screen.getAllByRole('row')).toHaveLength(7))
    expect(shownNames()).toEqual(['background-jobs', 'eu-apps', 'core-services', 'ci-tools', 'edge-proxy', 'staging-web'])
    expect(screen.queryAllByRole('article')).toHaveLength(0)
    expect(screen.queryByRole('button', { name: 'Cards' })).not.toBeInTheDocument()
  })

  it('shows environment and status filters with counts', async () => {
    renderApp('/clusters')
    await screen.findByRole('radio', { name: 'All 6' })
    const env = screen.getByRole('radiogroup', { name: 'Environment' })
    expect(within(env).getByRole('radio', { name: 'All 6' })).toBeChecked()
    expect(within(env).getByRole('radio', { name: 'Prod 1' })).toBeInTheDocument()
    expect(within(env).getByRole('radio', { name: 'Staging 1' })).toBeInTheDocument()
    expect(within(env).getByRole('radio', { name: 'Dev 4' })).toBeInTheDocument()
    const status = screen.getByRole('radiogroup', { name: 'Status' })
    expect(within(status).getByRole('radio', { name: 'Any status 6' })).toBeChecked()
    expect(within(status).getByRole('radio', { name: 'Problems 2' })).toBeInTheDocument()
    expect(within(status).getByRole('radio', { name: 'Deploying 1' })).toBeInTheDocument()
    expect(within(status).getByRole('radio', { name: 'Healthy 3' })).toBeInTheDocument()
  })

  it('environment and status segments narrow the table and live in the URL', async () => {
    const user = userEvent.setup()
    renderApp('/clusters')
    await screen.findByRole('radio', { name: 'Dev 4' })

    await user.click(screen.getByRole('radio', { name: 'Dev 4' }))
    await waitFor(() => expect(shownNames()).toHaveLength(4))
    expect(screen.getByTestId('search').textContent).toContain('env=dev')

    await user.click(screen.getByRole('radio', { name: 'Problems 2' }))
    await waitFor(() => expect(shownNames()).toEqual(['background-jobs', 'eu-apps']))
    expect(screen.getByTestId('search').textContent).toContain('status=problems')

    await user.click(screen.getByRole('radio', { name: 'Any status 6' }))
    await user.click(screen.getByRole('radio', { name: 'All 6' }))
    await waitFor(() => expect(shownNames()).toHaveLength(6))
    expect(screen.getByTestId('search').textContent).not.toMatch(/env=|status=/)
  })

  it('the text filter matches cluster, account or region and is kept in the URL', async () => {
    const user = userEvent.setup()
    renderApp('/clusters')
    await screen.findByRole('table', { name: 'Clusters' })
    await user.type(screen.getByRole('searchbox', { name: 'Filter clusters' }), 'staging-example')
    await waitFor(() => expect(shownNames()).toEqual(['staging-web']))
    expect(screen.getByTestId('search').textContent).toContain('q=staging-example')
  })

  it('restores every filter, the sort and the page from the URL', async () => {
    renderApp('/clusters?env=dev&status=healthy&q=c&sort=name:desc')
    await waitFor(() => expect(shownNames()).toEqual(['edge-proxy', 'ci-tools']))
    expect(screen.getByRole('radio', { name: /^Dev/ })).toBeChecked()
    expect(screen.getByRole('radio', { name: /^Healthy/ })).toBeChecked()
    expect(screen.getByRole('searchbox', { name: 'Filter clusters' })).toHaveValue('c')
    expect(screen.getByRole('columnheader', { name: /Cluster/ })).toHaveAttribute('aria-sort', 'descending')
  })

  it('accepts the old status values from earlier links', async () => {
    renderApp('/clusters?status=needs_attention')
    await screen.findByRole('table', { name: 'Clusters' })
    expect(screen.getByRole('radio', { name: /^Problems/ })).toBeChecked()
  })

  it('a header click sorts ascending, then descending, and records it in the URL', async () => {
    const user = userEvent.setup()
    renderApp('/clusters')
    await screen.findByRole('table', { name: 'Clusters' })
    const header = () => screen.getByRole('columnheader', { name: /Cluster/ })

    await user.click(within(header()).getByRole('button'))
    expect(shownNames()).toEqual(['background-jobs', 'ci-tools', 'core-services', 'edge-proxy', 'eu-apps', 'staging-web'])
    expect(header()).toHaveAttribute('aria-sort', 'ascending')
    expect(screen.getByTestId('search').textContent).toContain('sort=name%3Aasc')

    await user.click(within(header()).getByRole('button'))
    expect(shownNames()[0]).toBe('staging-web')
    expect(header()).toHaveAttribute('aria-sort', 'descending')
  })

  it('shows 25 clusters per page and keeps the page in the URL', async () => {
    const user = userEvent.setup()
    serveManyClusters(60)
    renderApp('/clusters')
    await screen.findByRole('table', { name: 'Clusters' })
    await waitFor(() => expect(screen.getAllByRole('row')).toHaveLength(26))
    expect(screen.getByText('1–25 of 60')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Next page' }))
    await waitFor(() => expect(screen.getByText('26–50 of 60')).toBeInTheDocument())
    expect(screen.getByTestId('search').textContent).toContain('page=2')
    expect(screen.getAllByRole('row')).toHaveLength(26)
  })

  it('opens the page from the URL and returns to page 1 when a filter changes', async () => {
    const user = userEvent.setup()
    serveManyClusters(60)
    renderApp('/clusters?page=3')
    expect(await screen.findByText('51–60 of 60')).toBeInTheDocument()
    await user.click(screen.getByRole('radio', { name: /^Healthy/ }))
    await waitFor(() => expect(screen.getByTestId('search').textContent).not.toContain('page='))
  })

  it('previews three services and counts the rest', async () => {
    const [first] = CLUSTERS.items
    const services = ['a-svc', 'b-svc', 'c-svc', 'd-svc', 'e-svc'].map((name) => ({ id: `id-${name}`, name, status: 'steady' as const }))
    server.use(http.get('/api/clusters', () => HttpResponse.json({ ...CLUSTERS, items: [{ ...first, services }] })))
    renderApp('/clusters')
    const row = within(await screen.findByRole('row', { name: new RegExp(first.name) }))
    expect(row.getByRole('link', { name: 'a-svc' })).toHaveAttribute('href', '/services/id-a-svc/overview')
    expect(row.getByRole('link', { name: 'c-svc' })).toBeInTheDocument()
    expect(row.queryByRole('link', { name: 'd-svc' })).not.toBeInTheDocument()
    expect(row.getByText('+2')).toBeInTheDocument()
  })

  it('puts problem services first in the preview', async () => {
    const [first] = CLUSTERS.items
    const services = [
      { id: 's1', name: 'calm-one', status: 'steady' as const },
      { id: 's2', name: 'calm-two', status: 'steady' as const },
      { id: 's3', name: 'broken', status: 'failing' as const },
    ]
    server.use(http.get('/api/clusters', () => HttpResponse.json({ ...CLUSTERS, items: [{ ...first, services }] })))
    renderApp('/clusters')
    const row = within(await screen.findByRole('row', { name: new RegExp(first.name) }))
    expect(row.getAllByRole('link').map((link) => link.textContent)).toEqual([first.name, 'broken', 'calm-one', 'calm-two'])
  })

  it('shows the 24 h CPU percent and a dash where Container Insights is off', async () => {
    renderApp('/clusters')
    const euApps = within(await screen.findByRole('row', { name: /eu-apps/ }))
    expect(await euApps.findByText('49%')).toBeInTheDocument()
    const edgeProxy = within(screen.getByRole('row', { name: /edge-proxy/ }))
    expect(edgeProxy.getAllByTitle('Container Insights is off').length).toBeGreaterThan(0)
    expect(screen.getByText(/A dash in CPU and memory means Container Insights is off/)).toBeInTheDocument()
  })

  it('shows an empty state whose button clears the filters', async () => {
    const user = userEvent.setup()
    renderApp('/clusters')
    await screen.findByRole('table', { name: 'Clusters' })
    await user.type(screen.getByRole('searchbox', { name: 'Filter clusters' }), 'nothing-like-this')
    const message = await screen.findByText('No clusters match these filters.')
    await user.click(within(message).getByRole('button', { name: 'Clear filters' }))
    await waitFor(() => expect(shownNames()).toHaveLength(6))
    expect(screen.getByTestId('search').textContent).not.toMatch(/q=/)
  })

  it('a scope filter that matches nothing shows the empty state', async () => {
    const user = userEvent.setup()
    renderApp('/clusters?tag=99999')
    const message = await screen.findByText('No clusters match these filters.')
    await user.click(within(message).getByRole('button', { name: 'Clear filters' }))
    await waitFor(() => expect(shownNames()).toHaveLength(6))
  })

  it("the top bar's account select narrows the list", async () => {
    const user = userEvent.setup()
    renderApp('/clusters')
    await screen.findByRole('table', { name: 'Clusters' })
    const select = screen.getByRole('combobox', { name: 'Account filter' })
    const staging = within(select).getByRole('option', { name: /staging/i }) as HTMLOptionElement
    await user.selectOptions(select, staging.value)
    await waitFor(() => expect(shownNames()).toEqual(['staging-web']))
    expect(screen.getByTestId('search').textContent).toContain('account=')
  })

  it('Clear filters also clears the remembered account, region and tag scope', async () => {
    const user = userEvent.setup()
    localStorage.setItem(
      'harbormaster-filters:pavel',
      JSON.stringify({ account: ACCOUNT_STAGING.account_id, region: 'us-east-1', tag: [TAG_STAGING.id] }),
    )
    renderApp(`/clusters?status=healthy&q=web&account=${ACCOUNT_STAGING.account_id}&region=us-east-1&tag=${TAG_STAGING.id}`)
    await waitFor(() => expect(shownNames()).toEqual(['staging-web']))

    await user.click(within(screen.getByRole('group', { name: 'Cluster filters' })).getByRole('button', { name: 'Clear filters' }))

    await waitFor(() => expect(shownNames()).toHaveLength(6))
    await waitFor(() => expect(screen.getByTestId('search').textContent).toBe(''))
    const stored = JSON.parse(localStorage.getItem('harbormaster-filters:pavel') ?? '{}')
    expect(stored.account).toBeUndefined()
    expect(stored.region).toBeUndefined()
    expect(stored.tag).toBeUndefined()
  })
})
