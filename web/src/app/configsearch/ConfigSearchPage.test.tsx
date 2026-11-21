import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { delay, http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ConfigSearchResponse } from '../../api/configSearch'
import {
  CONFIG_SEARCH_EMPTY,
  CONFIG_SEARCH_NODE,
  CONFIG_SEARCH_TRUNCATED,
  CONFIG_SEARCH_UNINDEXED,
} from '../../test/configSearchFixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})
afterEach(async () => {
  vi.unstubAllGlobals()
  await new Promise((resolve) => setTimeout(resolve, 100))
})

const NODE = '/config?q=env%3ANODE_ENV'

/** Replaces the search endpoint with `body` and records every request URL. */
function searchReturns(body: ConfigSearchResponse) {
  const urls: URL[] = []
  server.use(
    http.get('/api/config-search', ({ request }) => {
      urls.push(new URL(request.url))
      return HttpResponse.json(body)
    }),
  )
  return urls
}

const resultsTable = async () => screen.findByRole('table', { name: 'Config search results' })
const rowOf = (table: HTMLElement, name: string) => within(table).getByRole('link', { name }).closest('tr') as HTMLElement

function readBlob(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result))
    reader.onerror = () => reject(reader.error)
    reader.readAsText(blob)
  })
}

/** Captures what Export CSV downloads: every Blob handed to URL.createObjectURL. */
function captureDownloads() {
  const blobs: Blob[] = []
  vi.stubGlobal(
    'URL',
    Object.assign(URL, {
      createObjectURL: (blob: Blob) => {
        blobs.push(blob)
        return 'blob:test'
      },
      revokeObjectURL: () => {},
    }),
  )
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
  return blobs
}

describe('ConfigSearchPage', () => {
  it('shows examples and sends no request before a query', async () => {
    const urls = searchReturns(CONFIG_SEARCH_NODE)
    const user = userEvent.setup()
    renderApp('/config')
    expect(await screen.findByRole('heading', { level: 1, name: 'Config search' })).toBeInTheDocument()
    expect(screen.getByText(/Search the task definitions of the services you can see/)).toHaveTextContent(
      'Search the task definitions of the services you can see. Environment variable and secret values are never searched or shown, only their names.',
    )
    expect(screen.getByText('Base image is the one set for the repository on the Security page. It is not checked against the image here.')).toBeInTheDocument()
    for (const example of ['image:node:18', 'env:DATABASE_URL', 'port:8080']) {
      expect(screen.getByRole('button', { name: example })).toBeInTheDocument()
    }
    await user.type(screen.getByRole('textbox', { name: 'Config search query' }), 'env:PORT')
    await new Promise((resolve) => setTimeout(resolve, 150))
    expect(urls).toHaveLength(0)
  })

  it('runs the query from the URL and lists matching services', async () => {
    searchReturns(CONFIG_SEARCH_NODE)
    renderApp(NODE)
    const table = await resultsTable()
    expect(screen.getByRole('textbox', { name: 'Config search query' })).toHaveValue('env:NODE_ENV')
    expect(within(table).getByRole('link', { name: 'payments-api' })).toHaveAttribute('href', '/services/svc-payments/task-definition')
    expect(within(table).getAllByRole('link').map((link) => link.textContent)).toEqual(
      expect.arrayContaining(['api', 'notifications', 'payments-api']),
    )
    expect(screen.getByText('3 services match')).toBeInTheDocument()
    expect(screen.getByText('Searched 15 services you can see.')).toBeInTheDocument()
    // the first container's repository and tag, with a count of the others
    const payments = rowOf(table, 'payments-api')
    expect(within(payments).getByText('payments-api:1.18.2')).toBeInTheDocument()
    expect(within(payments).getByText('+1')).toBeInTheDocument()
    expect(within(payments).getByText('env NODE_ENV')).toBeInTheDocument()
  })

  it('submits on Enter and puts the query in the URL', async () => {
    searchReturns(CONFIG_SEARCH_NODE)
    const user = userEvent.setup()
    renderApp('/config')
    await user.type(await screen.findByRole('textbox', { name: 'Config search query' }), 'port:8080{Enter}')
    await waitFor(() => expect(decodeURIComponent(screen.getByTestId('search').textContent ?? '')).toContain('q=port:8080'))
    await resultsTable()
  })

  it('adds a field from a chip', async () => {
    const user = userEvent.setup()
    renderApp('/config')
    const input = await screen.findByRole('textbox', { name: 'Config search query' })
    await user.type(input, 'image:node:18')
    await user.click(screen.getByRole('button', { name: 'env:' }))
    expect(input).toHaveValue('image:node:18 env:')
    expect(input).toHaveFocus()
    await user.click(screen.getByRole('button', { name: 'env:' }))
    expect(input).toHaveValue('image:node:18 env:')
  })

  it('passes the shell filters to the API', async () => {
    const urls = searchReturns(CONFIG_SEARCH_NODE)
    renderApp(`${NODE}&account=482109375512&region=us-east-1`)
    await resultsTable()
    expect(urls.length).toBeGreaterThan(0)
    const first = urls[0]
    expect(first.searchParams.get('account')).toBe('482109375512')
    expect(first.searchParams.get('region')).toBe('us-east-1')
    expect(first.searchParams.get('q')).toBe('env:NODE_ENV')
  })

  it('labels a service matched by its previous revision', async () => {
    searchReturns(CONFIG_SEARCH_NODE)
    renderApp(NODE)
    const table = await resultsTable()
    const row = rowOf(table, 'notifications')
    expect(within(row).getByText('Previous revision :9 is still running')).toBeInTheDocument()
    expect(within(row).getByText(':10')).toBeInTheDocument()
    expect(within(rowOf(table, 'payments-api')).queryByText(/Previous revision/)).not.toBeInTheDocument()
  })

  it('expands a row to show containers, names and roles', async () => {
    searchReturns(CONFIG_SEARCH_NODE)
    const user = userEvent.setup()
    renderApp(NODE)
    await resultsTable()
    expect(screen.queryByText('DB_POOL_SIZE')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Details for payments-api' }))
    expect(screen.getByRole('button', { name: 'Details for payments-api' })).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText(/DB_POOL_SIZE/)).toBeInTheDocument()
    expect(screen.getByText('node:18-alpine')).toBeInTheDocument()
    expect(screen.getByText('/ecs/core-services/payments-api')).toBeInTheDocument()
    expect(screen.getByText('arn:aws:iam::482109375512:role/payments-api-task')).toBeInTheDocument()
    expect(screen.getByText('otel-collector')).toBeInTheDocument()
  })

  it('says when results are capped', async () => {
    searchReturns(CONFIG_SEARCH_TRUNCATED)
    renderApp(NODE)
    expect(await screen.findByText('Showing the first 2,000 of 2,400 matching services. Narrow the search to see the rest.')).toBeInTheDocument()
  })

  it('says how many services have no task definition loaded', async () => {
    searchReturns(CONFIG_SEARCH_UNINDEXED)
    renderApp(NODE)
    expect(await screen.findByText('2 services have no task definition loaded yet and were not searched.')).toBeInTheDocument()
  })

  it('shows the API message for an invalid query', async () => {
    renderApp('/config?q=port%3Ahttp')
    expect(await screen.findByRole('alert')).toHaveTextContent('port: needs a number from 1 to 65535.')
    expect(screen.queryByRole('table', { name: 'Config search results' })).not.toBeInTheDocument()
  })

  it('shows the empty result message', async () => {
    searchReturns(CONFIG_SEARCH_EMPTY)
    renderApp('/config?q=nomatch')
    expect(await screen.findByText('No services match.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Export CSV' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Copy service IDs' })).toBeDisabled()
  })

  it('exports the ticked rows as CSV', async () => {
    searchReturns(CONFIG_SEARCH_NODE)
    const blobs = captureDownloads()
    const user = userEvent.setup()
    renderApp(NODE)
    await resultsTable()
    await user.click(screen.getByRole('checkbox', { name: 'Select notifications' }))
    expect(screen.getByText('1 selected')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Export CSV' }))
    expect(blobs).toHaveLength(1)
    const text = await readBlob(blobs[0])
    expect(text).toContain('notifications,core-services')
    expect(text).not.toContain('payments-api,')
    expect(text.split('\r\n')[0]).toMatch(/^service,cluster,/)
  })

  it('does not offer the previous query\'s rows while a new query loads', async () => {
    server.use(
      http.get('/api/config-search', async ({ request }) => {
        const q = new URL(request.url).searchParams.get('q')
        if (q === 'port:8080') await delay(400)
        return HttpResponse.json(CONFIG_SEARCH_NODE)
      }),
    )
    const user = userEvent.setup()
    renderApp(NODE)
    await resultsTable()
    const input = screen.getByRole('textbox', { name: 'Config search query' })
    await user.clear(input)
    await user.type(input, 'port:8080{Enter}')
    await waitFor(() => expect(screen.queryByRole('table', { name: 'Config search results' })).not.toBeInTheDocument())
    expect(screen.queryByRole('button', { name: 'Export CSV' })).not.toBeInTheDocument()
    await resultsTable()
  })

  it('exports every row when none is ticked', async () => {
    searchReturns(CONFIG_SEARCH_NODE)
    const blobs = captureDownloads()
    const user = userEvent.setup()
    renderApp(NODE)
    await resultsTable()
    await user.click(screen.getByRole('button', { name: 'Export CSV' }))
    const text = await readBlob(blobs[0])
    for (const name of ['api,', 'notifications,', 'payments-api,']) expect(text).toContain(name)
  })

  it('copies service ids', async () => {
    searchReturns(CONFIG_SEARCH_NODE)
    const user = userEvent.setup()
    renderApp(NODE)
    const table = await resultsTable()
    await user.click(within(table).getByRole('checkbox', { name: 'Select payments-api' }))
    await user.click(within(table).getByRole('checkbox', { name: 'Select api' }))
    await user.click(screen.getByRole('button', { name: 'Copy service IDs' }))
    expect(await navigator.clipboard.readText()).toBe('svc-api\nsvc-payments')
    expect(await screen.findByText('Service IDs copied')).toBeInTheDocument()
  })

  it('selects every row from the header checkbox', async () => {
    searchReturns(CONFIG_SEARCH_NODE)
    const user = userEvent.setup()
    renderApp(NODE)
    await resultsTable()
    await user.click(screen.getByRole('checkbox', { name: 'Select all rows' }))
    expect(screen.getByText('3 selected')).toBeInTheDocument()
  })

  it('clears the selection when the query changes', async () => {
    searchReturns(CONFIG_SEARCH_NODE)
    const user = userEvent.setup()
    renderApp(NODE)
    await resultsTable()
    await user.click(screen.getByRole('checkbox', { name: 'Select notifications' }))
    expect(screen.getByText('1 selected')).toBeInTheDocument()
    const input = screen.getByRole('textbox', { name: 'Config search query' })
    await user.clear(input)
    await user.type(input, 'port:8080{Enter}')
    await waitFor(() => expect(screen.queryByText('1 selected')).not.toBeInTheDocument())
  })
})
