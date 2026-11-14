import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import type { LogSearchEstimateDTO, LogSearchRequest } from '../../api/logSearch'
import { LOG_SEARCH_ESTIMATE, LOG_SEARCH_RESULT, LOG_SEARCH_RESULT_SCAN_LIMIT } from '../../test/logSearchFixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})

const PAYMENTS = '/logs?services=svc-payments'

/** Replaces the estimate endpoint and records every request body it receives. */
function estimateReturns(estimate: LogSearchEstimateDTO) {
  const bodies: LogSearchRequest[] = []
  server.use(
    http.post('/api/log-search/estimate', async ({ request }) => {
      bodies.push((await request.json()) as LogSearchRequest)
      return HttpResponse.json(estimate)
    }),
  )
  return bodies
}

describe('Log search budget', () => {
  it('starts a new search on the 1 hour range', async () => {
    renderApp('/logs')
    const range = await screen.findByRole('group', { name: 'Range' })
    expect(within(range).getByRole('button', { name: '1 hour' })).toHaveAttribute('aria-pressed', 'true')
    expect(within(range).getByRole('button', { name: '24 hours' })).toHaveAttribute('aria-pressed', 'false')
  })

  it('shows a rough estimate, not a guaranteed upper bound, before the search is run', async () => {
    const bodies = estimateReturns(LOG_SEARCH_ESTIMATE)
    renderApp(PAYMENTS)
    const estimate = await screen.findByRole('status', { name: 'Scan estimate' })
    // storedBytes is compressed and Insights scans uncompressed data, so the copy must not promise a bound.
    expect(estimate).not.toHaveTextContent('at most')
    expect(estimate).toHaveTextContent('store about 4 GB')
    expect(estimate).toHaveTextContent('about $0.02')
    expect(estimate).toHaveTextContent('rough guide')
    expect(bodies).toHaveLength(1)
    expect(bodies[0].service_ids).toEqual(['svc-payments'])
    // Not run: no result and no run request.
    expect(screen.queryByText(/matching lines/)).not.toBeInTheDocument()
  })

  it('shows no estimate before a service is chosen', async () => {
    const bodies = estimateReturns(LOG_SEARCH_ESTIMATE)
    renderApp('/logs')
    await screen.findByRole('button', { name: 'Run query' })
    expect(screen.queryByRole('status', { name: 'Scan estimate' })).not.toBeInTheDocument()
    expect(bodies).toHaveLength(0)
  })

  it('warns that the search will stop at the scan limit when the estimate exceeds it', async () => {
    estimateReturns({ ...LOG_SEARCH_ESTIMATE, stored_bytes: 40 * 1024 ** 3, cost_usd: 0.2, scan_limit_bytes: 10 * 1024 ** 3, exceeds_scan_limit: true })
    renderApp(PAYMENTS)
    expect(await screen.findByText(/stop at the 10 GB scan limit/)).toBeInTheDocument()
  })

  it('does not warn when the estimate is under the scan limit', async () => {
    estimateReturns(LOG_SEARCH_ESTIMATE)
    renderApp(PAYMENTS)
    await screen.findByRole('status', { name: 'Scan estimate' })
    expect(screen.queryByText(/stop at the/)).not.toBeInTheDocument()
  })

  it('says how many log groups could not be sized', async () => {
    estimateReturns({ ...LOG_SEARCH_ESTIMATE, unknown_groups: 2 })
    renderApp(PAYMENTS)
    expect(await screen.findByText(/2 log groups could not be sized/)).toBeInTheDocument()
  })

  it('still lets the search run when the estimate fails', async () => {
    server.use(http.post('/api/log-search/estimate', () => HttpResponse.json({ code: 'internal', message: 'boom' }, { status: 500 })))
    renderApp(PAYMENTS)
    expect(await screen.findByRole('button', { name: 'Run query' })).toBeEnabled()
    expect(screen.queryByRole('status', { name: 'Scan estimate' })).not.toBeInTheDocument()
  })

  it('tells the user a result stopped at the scan limit is partial and keeps showing its cost', async () => {
    const user = userEvent.setup()
    server.use(http.post('/api/log-search', () => HttpResponse.json(LOG_SEARCH_RESULT_SCAN_LIMIT)))
    renderApp(PAYMENTS)
    await user.click(await screen.findByRole('button', { name: 'Run query' }))
    expect(await screen.findByText(/Stopped at the 10 GB scan limit/)).toBeInTheDocument()
    expect(screen.getByText(/10\.2 GB scanned/)).toBeInTheDocument()
  })

  it('does not show the scan limit notice for a complete result', async () => {
    const user = userEvent.setup()
    server.use(http.post('/api/log-search', () => HttpResponse.json(LOG_SEARCH_RESULT)))
    renderApp(PAYMENTS)
    await user.click(await screen.findByRole('button', { name: 'Run query' }))
    await screen.findByText('3 matching lines')
    expect(screen.queryByText(/scan limit/)).not.toBeInTheDocument()
  })
})
