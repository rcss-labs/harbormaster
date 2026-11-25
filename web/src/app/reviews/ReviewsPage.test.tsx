import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { REVIEW_RUN_DONE, mockReviews } from '../../test/reviewFixtures'
import { renderApp } from '../../test/renderApp'

const run = (id: number, overrides: object = {}) => ({ ...REVIEW_RUN_DONE, id, name: `Review ${id}`, ...overrides })

afterEach(() => vi.useRealTimers())

describe('ReviewsPage', () => {
  it('lists results and loads more', async () => {
    const requested = mockReviews([run(3), run(2, { type: 'security', state: 'skipped', reason: 'budget', summary: '' }), run(1)], true, 2)
    const user = userEvent.setup()
    renderApp('/reviews')

    const first = await screen.findByRole('link', { name: 'Review 3' })
    expect(first).toHaveAttribute('href', '/reviews/runs/3')
    const row = first.closest('[data-review-row]') as HTMLElement
    expect(within(row).getByText('Cost')).toBeInTheDocument()
    expect(within(row).getByText('Week of 16 Nov 2026')).toBeInTheDocument()
    expect(within(row).getByText('Review ready')).toBeInTheDocument()
    expect(within(row).getByText('Spend rose 12% on three services.')).toBeInTheDocument()
    expect(screen.queryByText(/A second line/)).not.toBeInTheDocument()
    expect(screen.getByText('Security findings')).toBeInTheDocument()
    expect(screen.getByText(/Not run: this month's AI allowance/)).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Review 1' })).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Load more' }))

    expect(await screen.findByRole('link', { name: 'Review 1' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument()
    expect(requested[requested.length - 1]).toContain('cursor=2')
  })

  it('shows a review name as characters', async () => {
    mockReviews()
    renderApp('/reviews')

    expect(await screen.findByRole('link', { name: 'Weekly <b>cost</b> check' })).toBeInTheDocument()
  })

  it('empty state', async () => {
    mockReviews([])
    renderApp('/reviews')

    expect(await screen.findByText('No review results yet.')).toBeInTheDocument()
  })

  it('lists the stored results and opens one while reviews are off and the agent is on', async () => {
    mockReviews([REVIEW_RUN_DONE], false, 50, true)
    const user = userEvent.setup()
    renderApp('/reviews')

    await user.click(await screen.findByRole('link', { name: REVIEW_RUN_DONE.name }))
    expect(await screen.findByText('Fargate hours doubled.')).toBeInTheDocument()
  })

  it('no request when the agent is off', async () => {
    const requested = mockReviews([REVIEW_RUN_DONE], false)
    renderApp('/reviews')

    expect(await screen.findByText('Page not found.')).toBeInTheDocument()
    expect(requested).toEqual([])
  })

  it('refreshes every 15 s while a row is waiting or running, and not otherwise', async () => {
    const listCalls = (requested: string[]) => requested.filter((url) => url.startsWith('/api/reviews/runs?') || url === '/api/reviews/runs').length
    vi.useFakeTimers({ shouldAdvanceTime: true })

    const busy = mockReviews([run(2, { state: 'running', summary: '' }), run(1)])
    const first = renderApp('/reviews')
    await screen.findByRole('link', { name: 'Review 2' })
    expect(listCalls(busy)).toBe(1)
    await vi.advanceTimersByTimeAsync(15_100)
    await waitFor(() => expect(listCalls(busy)).toBe(2))
    first.unmount()

    const idle = mockReviews([run(2), run(1)])
    renderApp('/reviews')
    await screen.findByRole('link', { name: 'Review 2' })
    await vi.advanceTimersByTimeAsync(40_000)
    expect(listCalls(idle)).toBe(1)
  })
})
