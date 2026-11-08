import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { delay, http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import type { LogSearchRequest, LogSearchResultDTO, LogSearchRowDTO, SaveLogSearchBody } from '../../api/logSearch'
import {
  LOG_SEARCH_RESULT,
  LOG_SEARCH_RESULT_CAPPED,
  LOG_SEARCH_RESULT_NO_PERMISSION,
  LOG_SEARCH_SERVICES,
} from '../../test/logSearchFixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})
afterEach(async () => {
  await new Promise((resolve) => setTimeout(resolve, 100))
})

const BOTH = '/logs?services=svc-payments,svc-catalog'

/** Replaces the run endpoint and records every request body it receives. */
function runReturns(result: LogSearchResultDTO) {
  const bodies: LogSearchRequest[] = []
  server.use(
    http.post('/api/log-search', async ({ request }) => {
      bodies.push((await request.json()) as LogSearchRequest)
      return HttpResponse.json(result)
    }),
  )
  return bodies
}

const two = (n: number) => String(n).padStart(2, '0')
/** The time a line shows: the browser's local zone, like the Logs tab. */
const localClock = (iso: string) => {
  const d = new Date(iso)
  return `${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}.${String(d.getMilliseconds()).padStart(3, '0')}`
}

const run = async (user: ReturnType<typeof userEvent.setup>) => user.click(await screen.findByRole('button', { name: 'Run query' }))

describe('Log search form', () => {
  it('lists only the services from the API in the picker', async () => {
    const user = userEvent.setup()
    renderApp('/logs')
    await user.click(await screen.findByRole('combobox', { name: 'Add service' }))
    const options = await screen.findAllByRole('option')
    expect(options.map((option) => option.textContent)).toEqual(expect.arrayContaining([expect.stringMatching(/^payments/), expect.stringMatching(/^catalog/)]))
    expect(options).toHaveLength(LOG_SEARCH_SERVICES.items.length)

    await user.type(screen.getByRole('combobox', { name: 'Add service' }), 'pay')
    expect(screen.getAllByRole('option')).toHaveLength(1)
    await user.click(screen.getByRole('option', { name: /^payments/ }))
    const chips = screen.getByRole('list', { name: 'Selected services' })
    expect(within(chips).getByText('payments')).toBeInTheDocument()
    expect(screen.getByTestId('search')).toHaveTextContent('services=svc-payments')
    await user.click(within(chips).getByRole('button', { name: 'Remove payments' }))
    expect(within(screen.getByRole('list', { name: 'Selected services' })).queryByText('payments')).not.toBeInTheDocument()
  })

  it('fills the form from the URL without running', async () => {
    const bodies = runReturns(LOG_SEARCH_RESULT)
    const q = encodeURIComponent('filter @message like /timeout/')
    renderApp(`${BOTH}&range=24h&q=${q}`)
    const chips = await screen.findByRole('list', { name: 'Selected services' })
    expect(await within(chips).findByText('payments')).toBeInTheDocument()
    expect(within(chips).getByText('catalog')).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: 'Query' })).toHaveValue('filter @message like /timeout/')
    expect(within(screen.getByRole('group', { name: 'Range' })).getByRole('button', { name: '24 hours' })).toHaveAttribute('aria-pressed', 'true')
    await new Promise((resolve) => setTimeout(resolve, 150))
    expect(bodies).toHaveLength(0)
    expect(screen.queryByText(/matching lines/)).not.toBeInTheDocument()
  })

  it('disables Run query with fewer than one service and explains why', async () => {
    const user = userEvent.setup()
    renderApp('/logs')
    const button = await screen.findByRole('button', { name: 'Run query' })
    expect(button).toBeDisabled()
    expect(screen.getByText('Choose at least one service.')).toBeInTheDocument()
    await user.click(screen.getByRole('combobox', { name: 'Add service' }))
    await user.click(await screen.findByRole('option', { name: /^payments/ }))
    expect(screen.getByRole('button', { name: 'Run query' })).toBeEnabled()
    expect(screen.queryByText('Choose at least one service.')).not.toBeInTheDocument()
  })

  it('shows the empty state when no services are readable', async () => {
    server.use(http.get('/api/log-search/services', () => HttpResponse.json({ ...LOG_SEARCH_SERVICES, items: [] })))
    renderApp('/logs')
    expect(await screen.findByText('You have no services you can read logs of.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Run query' })).not.toBeInTheDocument()
  })

  it('re-reads the form when the URL changes while mounted', async () => {
    const user = userEvent.setup()
    renderApp('/logs?services=svc-payments&q=abc')
    expect(await screen.findByRole('textbox', { name: 'Query' })).toHaveValue('abc')
    await user.click(screen.getByRole('link', { name: /^Log search/ }))
    await waitFor(() => expect(screen.getByRole('textbox', { name: 'Query' })).toHaveValue(''))
    expect(within(screen.getByRole('list', { name: 'Selected services' })).queryByText('payments')).not.toBeInTheDocument()
  })

  it('asks for a start and end on the custom range and writes them to the URL', async () => {
    const user = userEvent.setup()
    renderApp(`${BOTH}`)
    await user.click(await screen.findByRole('button', { name: 'Custom' }))
    const from = await screen.findByLabelText('From')
    const to = screen.getByLabelText('To')
    fireEvent.change(from, { target: { value: '2026-11-08T09:00' } })
    fireEvent.change(to, { target: { value: '2026-11-08T10:00' } })
    await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent(/range=custom.*from=.*to=/))
  })
})

describe('Log search results', () => {
  it('runs and shows count, bytes, cost, histogram, hits by service and lines', async () => {
    const user = userEvent.setup()
    const bodies = runReturns(LOG_SEARCH_RESULT)
    renderApp(`${BOTH}&q=${encodeURIComponent('filter @message like /ERROR/')}`)
    await run(user)

    expect(await screen.findByText('3 matching lines')).toBeInTheDocument()
    expect(screen.getByText('1.4 GB scanned in 2.4 s · less than $0.01')).toBeInTheDocument()
    expect(bodies).toEqual([{ service_ids: ['svc-payments', 'svc-catalog'], range: '1h', query: 'filter @message like /ERROR/' }])
    expect(screen.getByRole('img', { name: 'Hits over time' })).toBeInTheDocument()
    const hits = within(screen.getByRole('region', { name: 'Hits by service' }))
    expect(hits.getByText('payments').parentElement?.parentElement).toHaveTextContent(/payments.*2/)
    expect(hits.getByText('catalog').parentElement?.parentElement).toHaveTextContent(/catalog.*1/)
    const lines = within(screen.getByRole('list', { name: 'Matching lines' })).getAllByRole('link')
    expect(lines).toHaveLength(3)
    expect(lines[0]).toHaveTextContent(localClock('2026-11-08T10:00:03.250Z'))
    expect(lines[0]).toHaveTextContent('payments')
    expect(lines[0]).toHaveTextContent('ERROR card declined')
  })

  it('says when results are capped', async () => {
    const user = userEvent.setup()
    runReturns(LOG_SEARCH_RESULT_CAPPED)
    renderApp(BOTH)
    await run(user)
    expect(await screen.findByText('48,210 matching lines')).toBeInTheDocument()
    expect(screen.getByText(/Showing the newest 3 of 48,210 matching lines\. The histogram and hits by service cover the lines shown\./)).toBeInTheDocument()
  })

  it('shows the per-account permission message', async () => {
    const user = userEvent.setup()
    runReturns(LOG_SEARCH_RESULT_NO_PERMISSION)
    renderApp(BOTH)
    await run(user)
    const message = LOG_SEARCH_RESULT_NO_PERMISSION.targets[0].message
    expect(await screen.findByText(message)).toBeInTheDocument()
    expect(screen.getByText('No lines matched.')).toBeInTheDocument()
  })

  it('notes a service that has no container logging to CloudWatch', async () => {
    const user = userEvent.setup()
    runReturns({
      ...LOG_SEARCH_RESULT,
      services: [LOG_SEARCH_RESULT.services[0], { id: 'svc-catalog', name: 'catalog', state: 'no_awslogs', message: '' }],
    })
    renderApp(BOTH)
    await run(user)
    expect(await screen.findByText('catalog has no container that logs to CloudWatch.')).toBeInTheDocument()
  })

  it('opens the Logs tab at the line\'s second', async () => {
    const user = userEvent.setup()
    runReturns(LOG_SEARCH_RESULT)
    renderApp(BOTH)
    await run(user)
    await user.click(await screen.findByRole('link', { name: /ERROR upstream timeout/ }))
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent('/services/svc-catalog/logs'))
    expect(screen.getByTestId('search')).toHaveTextContent(`before=${encodeURIComponent('2026-11-08T10:00:03.100Z')}`)
  })

  it('shows 200 lines first and Show more adds 200', async () => {
    const user = userEvent.setup()
    const rows: LogSearchRowDTO[] = Array.from({ length: 450 }, (_, i) => ({ ...LOG_SEARCH_RESULT.rows[0], message: `line ${i}` }))
    runReturns({ ...LOG_SEARCH_RESULT, rows, records_matched: 450 })
    renderApp(BOTH)
    await run(user)
    await screen.findByText('450 matching lines')
    const count = () => within(screen.getByRole('list', { name: 'Matching lines' })).getAllByRole('link').length
    expect(count()).toBe(200)
    await user.click(screen.getByRole('button', { name: 'Show more' }))
    expect(count()).toBe(400)
    await user.click(screen.getByRole('button', { name: 'Show more' }))
    expect(count()).toBe(450)
    expect(screen.queryByRole('button', { name: 'Show more' })).not.toBeInTheDocument()
  })

  it('uses the singular for one matching line', async () => {
    const user = userEvent.setup()
    runReturns({ ...LOG_SEARCH_RESULT, rows: LOG_SEARCH_RESULT.rows.slice(0, 1), records_matched: 1 })
    renderApp(BOTH)
    await run(user)
    expect(await screen.findByText('1 matching line')).toBeInTheDocument()
  })

  it('shows the empty message when nothing matched', async () => {
    const user = userEvent.setup()
    runReturns({ ...LOG_SEARCH_RESULT, rows: [], records_matched: 0 })
    renderApp(BOTH)
    await run(user)
    expect(await screen.findByText('No lines matched.')).toBeInTheDocument()
  })

  it('cancels a running query', async () => {
    const user = userEvent.setup()
    server.use(
      http.post('/api/log-search', async () => {
        await delay('infinite')
        return HttpResponse.json(LOG_SEARCH_RESULT)
      }),
    )
    renderApp(BOTH)
    await run(user)
    const cancel = await screen.findByRole('button', { name: 'Cancel' })
    expect(screen.queryByRole('button', { name: 'Run query' })).not.toBeInTheDocument()
    await user.click(cancel)
    expect(await screen.findByRole('button', { name: 'Run query' })).toBeEnabled()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})

describe('Log search errors', () => {
  const fail = (status: number, message: string) =>
    server.use(http.post('/api/log-search', () => HttpResponse.json({ error: { code: 'invalid', message } }, { status })))

  it.each([
    [404, 'One of the services was not found.'],
    [403, 'You do not have access to one of the services.'],
    [409, 'You already have a log search running.'],
    [429, 'Too many log searches. Try again in a minute.'],
    [422, 'Logs Insights command "stats" is not allowed here.'],
  ])('shows the API message for a %i and keeps the form', async (status, message) => {
    const user = userEvent.setup()
    fail(status, message)
    renderApp(`${BOTH}&q=stats%20count()`)
    await run(user)
    expect(await screen.findByRole('alert')).toHaveTextContent(message)
    expect(screen.getByRole('textbox', { name: 'Query' })).toHaveValue('stats count()')
    expect(within(screen.getByRole('list', { name: 'Selected services' })).getByText('payments')).toBeInTheDocument()
  })

  it('explains the quote rule on a 422', async () => {
    const user = userEvent.setup()
    fail(422, 'The query is not allowed.')
    renderApp(`${BOTH}&q=filter%20x`)
    await run(user)
    await screen.findByRole('alert')
    expect(screen.getByText(/only fields, filter, parse, dedup, sort and limit/)).toBeInTheDocument()
    expect(screen.getByText(/switch to the other quote type/)).toBeInTheDocument()
  })
})

describe('Saved log searches', () => {
  it('saves, loads and deletes a saved query', async () => {
    const user = userEvent.setup()
    const saves: SaveLogSearchBody[] = []
    const runs = runReturns(LOG_SEARCH_RESULT)
    const deleted: string[] = []
    server.use(
      http.post('/api/log-search/saved', async ({ request }) => {
        saves.push((await request.json()) as SaveLogSearchBody)
        return HttpResponse.json({ id: 'saved-3', ...saves[0], hidden_services: 0, from: null, to: null, updated_at: '2026-11-08T10:00:00Z' })
      }),
      http.delete('/api/log-search/saved/:id', ({ params }) => {
        deleted.push(String(params.id))
        return new HttpResponse(null, { status: 204 })
      }),
    )
    renderApp(`/logs?services=svc-catalog&q=${encodeURIComponent('filter x')}`)

    // Save: an existing name warns that it replaces that query.
    await user.click(await screen.findByRole('button', { name: 'Save this query' }))
    const dialog = await screen.findByRole('dialog')
    const name = within(dialog).getByRole('textbox', { name: 'Name' })
    await user.type(name, 'Payment errors')
    expect(within(dialog).getByText('This replaces the saved query "Payment errors".')).toBeInTheDocument()
    await user.clear(name)
    await user.type(name, 'Catalog watch')
    expect(within(dialog).queryByText(/This replaces/)).not.toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(saves).toEqual([{ name: 'Catalog watch', query: 'filter x', service_ids: ['svc-catalog'], range: '1h', from: null, to: null }]))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())

    // Load: fills the form and the URL, never runs, and says what is no longer available.
    const panel = within(await screen.findByRole('region', { name: 'Saved queries' }))
    expect(panel.getByText('Payment errors')).toBeInTheDocument()
    expect(panel.getAllByText('1 service')).toHaveLength(2)
    await user.click(panel.getByRole('button', { name: 'Load Payment errors' }))
    expect(screen.getByRole('textbox', { name: 'Query' })).toHaveValue('filter @message like /ERROR/')
    expect(within(screen.getByRole('list', { name: 'Selected services' })).getByText('payments')).toBeInTheDocument()
    expect(screen.getByTestId('search')).toHaveTextContent('services=svc-payments')
    expect(screen.getByText('1 service in this query is no longer available to you.')).toBeInTheDocument()
    expect(runs).toHaveLength(0)

    // Editing the services clears that note.
    await user.click(within(screen.getByRole('list', { name: 'Selected services' })).getByRole('button', { name: 'Remove payments' }))
    expect(screen.queryByText(/no longer available to you/)).not.toBeInTheDocument()
    await user.click(panel.getByRole('button', { name: 'Load Payment errors' }))

    // Delete: asks first.
    await user.click(panel.getByRole('button', { name: 'Delete Morning checkout' }))
    const confirm = await screen.findByRole('dialog')
    expect(deleted).toEqual([])
    await user.click(within(confirm).getByRole('button', { name: 'Delete' }))
    await waitFor(() => expect(deleted).toEqual(['saved-2']))
  })
})
