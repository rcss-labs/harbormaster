import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { SERVICES_LIST } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'

beforeEach(() => {
  localStorage.clear()
})

// Let requests still in flight from an unmounted app finish, so they cannot reach the next test's handlers.
afterEach(async () => {
  await new Promise((resolve) => setTimeout(resolve, 100))
})

const table = () => screen.findByRole('table', { name: 'Services' })
/** Names of the body rows, in display order (the first link of each row). */
const shownNames = () =>
  screen
    .getAllByRole('row')
    .slice(1)
    .map((row) => within(row).getAllByRole('link')[0].textContent)

describe('Services page', () => {
  it('lists every service with the worst first', async () => {
    renderApp('/services')
    await table()
    await waitFor(() => expect(screen.getAllByRole('row')).toHaveLength(16))
    expect(shownNames()[0]).toBe('image-resizer')
  })

  it('has saved view tabs with counts from the list', async () => {
    renderApp('/services')
    const views = await screen.findByRole('group', { name: 'Saved views' })
    expect(within(views).getAllByRole('button').map((b) => b.textContent)).toEqual(
      expect.arrayContaining(['All 15', 'Problems 3', 'My pins 0']),
    )
    expect(within(views).getByRole('button', { name: /^Prod / })).toBeInTheDocument()
    expect(within(views).getByRole('button', { name: 'All 15' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('a view narrows the table and lives in the URL', async () => {
    const user = userEvent.setup()
    renderApp('/services')
    await user.click(await screen.findByRole('button', { name: 'Problems 3' }))
    await waitFor(() => expect(shownNames()).toEqual(['image-resizer', 'catalog-api', 'notifications']))
    expect(screen.getByTestId('search').textContent).toContain('view=problems')
  })

  it('My pins lists services pinned in this browser', async () => {
    const user = userEvent.setup()
    renderApp('/services')
    await table()
    await user.click(await screen.findByRole('button', { name: 'Pin payments-api' }))
    await user.click(screen.getByRole('button', { name: 'My pins 1' }))
    await waitFor(() => expect(shownNames()).toEqual(['payments-api']))
  })

  it('status segment counts follow the view and filter the table', async () => {
    const user = userEvent.setup()
    renderApp('/services')
    const status = await screen.findByRole('radiogroup', { name: 'Status' })
    expect(within(status).getAllByRole('radio').map((r) => r.textContent)).toEqual(
      expect.arrayContaining(['All 15', 'Failing 1', 'Warning 1', 'In progress 1', 'Steady 12']),
    )
    await user.click(within(status).getByRole('radio', { name: 'Failing 1' }))
    await waitFor(() => expect(shownNames()).toEqual(['image-resizer']))
    expect(screen.getByTestId('search').textContent).toContain('status=bad')
  })

  it('the text filter matches and is kept in the URL; an empty result offers Clear filters', async () => {
    const user = userEvent.setup()
    renderApp('/services')
    await table()
    const box = screen.getByRole('searchbox', { name: 'Filter services' })
    expect(box).toHaveAttribute('data-hm-filter')
    await user.type(box, 'payments')
    await waitFor(() => expect(shownNames()).toEqual(['payments-api']))
    expect(screen.getByTestId('search').textContent).toContain('q=payments')
    await user.clear(box)
    await user.type(box, 'zzz')
    expect(await screen.findByText('No services match these filters.')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Clear filters' }))
    await waitFor(() => expect(shownNames()).toHaveLength(15))
  })

  it('column picker hides Image tag and persists', async () => {
    const user = userEvent.setup()
    const first = renderApp('/services')
    await table()
    expect(screen.getByRole('columnheader', { name: /Image tag/ })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: 'CPU' })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: 'Memory' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Columns' }))
    await user.click(screen.getByRole('checkbox', { name: 'Image tag' }))
    expect(screen.queryByRole('columnheader', { name: /Image tag/ })).not.toBeInTheDocument()
    expect(JSON.parse(localStorage.getItem('harbormaster-columns:services') ?? '[]')).not.toContain('image')
    first.unmount()
    renderApp('/services')
    await table()
    expect(screen.queryByRole('columnheader', { name: /Image tag/ })).not.toBeInTheDocument()
  })

  it('shows 25 per page with a pager', async () => {
    const user = userEvent.setup()
    const items = Array.from({ length: 60 }, (_, i) => ({
      ...SERVICES_LIST.items[i % 15],
      id: `gen-${i}`,
      name: `gen-${String(i).padStart(2, '0')}`,
    }))
    server.use(http.get('/api/services', () => HttpResponse.json({ ...SERVICES_LIST, items, total: 60 })))
    renderApp('/services?sort=name&dir=asc')
    await table()
    await waitFor(() => expect(screen.getAllByRole('row')).toHaveLength(26))
    expect(screen.getByText('1–25 of 60')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Next page' }))
    await waitFor(() => expect(shownNames()[0]).toBe('gen-25'))
    expect(screen.getByTestId('search').textContent).toContain('page=2')
  })

  describe('with 60 services', () => {
    const items = Array.from({ length: 60 }, (_, i) => ({
      ...SERVICES_LIST.items[i % 15],
      id: `gen-${i}`,
      name: `gen-${String(i).padStart(2, '0')}`,
    }))
    const metricRequests: string[][] = []
    beforeEach(() => {
      metricRequests.length = 0
      server.use(
        http.get('/api/services', () => HttpResponse.json({ ...SERVICES_LIST, items, total: 60 })),
        http.get('/api/services/metrics', ({ request }) => {
          metricRequests.push(new URL(request.url).searchParams.getAll('id'))
          return HttpResponse.json({ items: {}, errors: [], stale: null, polled_at: '2026-11-06T10:00:05Z' })
        }),
      )
    })

    it('requests metrics only for the page ids', async () => {
      const user = userEvent.setup()
      renderApp('/services?sort=name&dir=asc')
      await waitFor(() => expect(metricRequests.length).toBeGreaterThan(0))
      expect(metricRequests[0]).toHaveLength(25)
      expect(metricRequests[0]).toContain('gen-0')
      expect(metricRequests[0]).not.toContain('gen-25')
      await user.click(screen.getByRole('button', { name: 'Next page' }))
      await waitFor(() => expect(metricRequests.at(-1)).toContain('gen-25'))
      expect(metricRequests.at(-1)).not.toContain('gen-0')
      expect(metricRequests.every((ids) => ids.length <= 25)).toBe(true)
    })

    it('metrics refetch does not reset the page', async () => {
      const user = userEvent.setup()
      const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
      renderApp('/services?sort=name&dir=asc', queryClient)
      await table()
      await user.click(await screen.findByRole('button', { name: 'Next page' }))
      await waitFor(() => expect(shownNames()[0]).toBe('gen-25'))
      const before = metricRequests.length
      await queryClient.refetchQueries({ queryKey: ['services', 'metrics'] })
      await waitFor(() => expect(metricRequests.length).toBeGreaterThan(before))
      expect(shownNames()[0]).toBe('gen-25')
      expect(screen.getByTestId('search').textContent).toContain('page=2')
    })
  })

  it('clicking a column header sorts and writes sort and dir to the URL', async () => {
    const user = userEvent.setup()
    renderApp('/services')
    await table()
    await user.click(screen.getByRole('button', { name: /^Service/ }))
    await waitFor(() => expect(shownNames()[0]).toBe('api'))
    expect(screen.getByTestId('search').textContent).toMatch(/sort=name/)
    expect(screen.getByTestId('search').textContent).toMatch(/dir=asc/)
  })

  it('row Restart is disabled with the rollout tooltip when a rollout is in progress', async () => {
    renderApp('/services')
    await table()
    const restart = await screen.findByRole('button', { name: 'Restart notifications' })
    expect(restart).toBeDisabled()
    expect(restart).toHaveAttribute('title', 'A rollout is already running')
    expect(screen.getByRole('button', { name: 'Restart payments-api' })).toBeEnabled()
  })

  it('row Restart opens the confirm dialog', async () => {
    const user = userEvent.setup()
    renderApp('/services')
    await user.click(await screen.findByRole('button', { name: 'Restart payments-api' }))
    expect(await screen.findByText('Restart payments-api?')).toBeInTheDocument()
  })

  it('Details opens the drawer with the KV rows and six links; Esc closes it', async () => {
    const user = userEvent.setup()
    renderApp('/services')
    await user.click(await screen.findByRole('button', { name: 'Details for payments-api' }))
    const drawer = await screen.findByRole('dialog')
    for (const label of ['Cluster', 'Account / region', 'Tag', 'Tasks', 'Revision', 'Image tag', 'Last change']) {
      expect(within(drawer).getByText(label, { selector: 'dt' })).toBeInTheDocument()
    }
    const open = within(drawer).getByRole('group', { name: 'Open' })
    expect(within(open).getAllByRole('link').map((l) => l.textContent)).toEqual(['Overview', 'Tasks', 'Logs', 'Metrics', 'Deploy', 'History'])
    expect(within(open).getByRole('link', { name: 'History' })).toHaveAttribute('href', expect.stringMatching(/\/operations$/))
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('notes when the list was truncated', async () => {
    server.use(http.get('/api/services', () => HttpResponse.json({ ...SERVICES_LIST, truncated: true })))
    renderApp('/services')
    expect(await screen.findByText(/Only the first 2000 services are listed/)).toBeInTheDocument()
  })

  it('shows a retryable error when the list cannot be loaded', async () => {
    server.use(http.get('/api/services', () => HttpResponse.json({ error: 'boom' }, { status: 500 })))
    renderApp('/services')
    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
  })
})
