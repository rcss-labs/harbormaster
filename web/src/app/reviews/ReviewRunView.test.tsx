import { screen, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { REVIEW_RUN_DONE, mockReviews } from '../../test/reviewFixtures'
import { ME_ADMIN } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

afterEach(() => vi.useRealTimers())

describe('ReviewRunView', () => {
  it('shows scope, period and state', async () => {
    mockReviews()
    renderApp(`/reviews/runs/${REVIEW_RUN_DONE.id}`)

    expect(await screen.findByText('Weekly <b>cost</b> check')).toBeInTheDocument()
    expect(screen.getByText('Cost')).toBeInTheDocument()
    expect(screen.getByText('Week of 16 Nov 2026')).toBeInTheDocument()
    expect(screen.getByText('Tag: production')).toBeInTheDocument()
    expect(screen.getByText('12 services in scope')).toBeInTheDocument()
    expect(screen.getByText('Review ready')).toBeInTheDocument()
    expect(screen.getByText(/Spend rose 12% on three services\./)).toBeInTheDocument()
    expect(screen.getByText('payments: 2 -> 4 tasks').tagName).toBe('PRE')
  })

  it('says one service in scope in the singular', async () => {
    mockReviews([{ ...REVIEW_RUN_DONE, services_total: 1 }])
    renderApp(`/reviews/runs/${REVIEW_RUN_DONE.id}`)

    expect(await screen.findByText('1 service in scope')).toBeInTheDocument()
  })

  it('polls while running and stops when done', async () => {
    let calls = 0
    server.use(
      http.get('/api/me', () => HttpResponse.json({ ...ME_ADMIN, agent_enabled: true, reviews_enabled: true })),
      http.get('/api/reviews/runs/:id', () => {
        calls++
        return HttpResponse.json(calls < 2 ? { ...REVIEW_RUN_DONE, state: 'running', summary: '', finished_at: null, evidence: [] } : REVIEW_RUN_DONE)
      }),
    )
    vi.useFakeTimers({ shouldAdvanceTime: true })
    renderApp(`/reviews/runs/${REVIEW_RUN_DONE.id}`)

    expect(await screen.findByText('Running')).toBeInTheDocument()
    await vi.advanceTimersByTimeAsync(5_100)
    expect(await screen.findByText('Review ready')).toBeInTheDocument()
    const settled = calls
    await vi.advanceTimersByTimeAsync(20_000)
    expect(calls).toBe(settled)
  })

  it('gives up polling after 30 minutes', async () => {
    let calls = 0
    server.use(
      http.get('/api/me', () => HttpResponse.json({ ...ME_ADMIN, agent_enabled: true, reviews_enabled: true })),
      http.get('/api/reviews/runs/:id', () => {
        calls++
        return HttpResponse.json({ ...REVIEW_RUN_DONE, state: 'running', summary: '', finished_at: null, evidence: [] })
      }),
    )
    vi.useFakeTimers({ shouldAdvanceTime: true })
    renderApp(`/reviews/runs/${REVIEW_RUN_DONE.id}`)

    expect(await screen.findByText('Running')).toBeInTheDocument()
    await vi.advanceTimersByTimeAsync(31 * 60_000)
    await waitFor(() => expect(calls).toBeGreaterThan(1))
    const settled = calls
    await vi.advanceTimersByTimeAsync(60_000)
    expect(calls).toBe(settled)
  })

  it('not found', async () => {
    mockReviews()
    renderApp('/reviews/runs/999')

    expect(await screen.findByText('Page not found.')).toBeInTheDocument()
  })

  it('tells a refusal and an unavailable agent apart from not found', async () => {
    mockReviews()
    server.use(http.get('/api/reviews/runs/:id', () => HttpResponse.json({ error: { code: 'token_not_allowed', message: 'No.' } }, { status: 403 })))
    const { unmount } = renderApp('/reviews/runs/31')
    expect(await screen.findByText('You do not have permission to read this review.')).toBeInTheDocument()
    unmount()

    server.use(http.get('/api/reviews/runs/:id', () => HttpResponse.json({ error: { code: 'agent_unavailable', message: 'Down.' } }, { status: 503 })))
    renderApp('/reviews/runs/31')
    expect(await screen.findByText('The agent is not available right now.')).toBeInTheDocument()
  })
})
