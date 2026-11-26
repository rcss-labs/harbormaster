import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import type { ReviewInput } from '../../../api/agent'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'
import { REVIEW_CUSTOM, REVIEW_MONTHLY_SECURITY, REVIEW_SETTINGS, REVIEW_SETTINGS_CUSTOM, REVIEW_WEEKLY_COST, useReviewMocks } from '../../../test/reviewSettingsFixtures'

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})

const SLOW = { timeout: 4000 }
const reviewsPanel = async () => within(await screen.findByRole('region', { name: 'Reviews' }, SLOW))
const rowOf = async (name: string) => {
  const panel = await reviewsPanel()
  return within((await panel.findByText(name, undefined, SLOW)).closest('tr') as HTMLElement)
}

describe('ReviewsSection', () => {
  it('lists reviews with next run and last result', async () => {
    useReviewMocks()
    renderApp('/settings/agent')
    const weekly = await rowOf('Weekly cost')
    expect(weekly.getByText('Cost')).toBeInTheDocument()
    expect(weekly.getByText('Weekly on Monday at 08:00, Europe/Berlin')).toBeInTheDocument()
    // 07:00 UTC is 08:00 in Berlin in November.
    expect(weekly.getByText(/08:00 Europe\/Berlin/)).toBeInTheDocument()
    expect(weekly.getByRole('link', { name: /Done/ })).toHaveAttribute('href', '/reviews/runs/41')
    expect(weekly.getByRole('switch', { name: 'Weekly cost is on' })).toBeChecked()
    for (const action of ['Run now', 'Edit', 'Delete']) expect(weekly.getByRole('button', { name: new RegExp(`^${action}`) })).toBeInTheDocument()

    const monthly = await rowOf('Monthly security')
    expect(monthly.getByText('Security findings')).toBeInTheDocument()
    expect(monthly.getByText('Monthly on the 15th at 06:30, UTC')).toBeInTheDocument()
    expect(monthly.getByText('No results yet')).toBeInTheDocument()
    expect(monthly.getByRole('switch', { name: 'Monthly security is on' })).not.toBeChecked()
  })

  it('shows a state or type this version does not know as text', async () => {
    useReviewMocks({
      reviews: [{ ...REVIEW_WEEKLY_COST, type: 'forecast', last_run: { id: 9, state: 'paused', reason: '', finished_at: null } }],
    })
    renderApp('/settings/agent')
    const row = await rowOf('Weekly cost')
    expect(row.getByText('forecast')).toBeInTheDocument()
    expect(row.getByRole('link', { name: /paused/ })).toHaveAttribute('href', '/reviews/runs/9')
  })

  it('shows the off notice', async () => {
    useReviewMocks({ settings: { ...REVIEW_SETTINGS, reviews: { ...REVIEW_SETTINGS.reviews, enabled: false } } })
    renderApp('/settings/agent')
    expect(await screen.findByText('Reviews are turned off by configuration.', undefined, SLOW)).toBeInTheDocument()
  })

  it('shows the no-model notice', async () => {
    const { reviewer, ...roles } = REVIEW_SETTINGS.role_models
    void reviewer
    useReviewMocks({ settings: { ...REVIEW_SETTINGS, role_models: roles, reviews: { ...REVIEW_SETTINGS.reviews, worst_case_run_usd: null } } })
    renderApp('/settings/agent')
    expect(await screen.findByText('Set a model for reviews before they can run.', undefined, SLOW)).toBeInTheDocument()
    expect(screen.queryByText(/Worst case of one run/)).not.toBeInTheDocument()
  })

  it('says what one run costs at worst and hides the notices when all is set', async () => {
    useReviewMocks()
    renderApp('/settings/agent')
    expect(await screen.findByText('Worst case of one run: $0.84.', undefined, SLOW)).toBeInTheDocument()
    expect(screen.queryByText('Reviews are turned off by configuration.')).not.toBeInTheDocument()
    expect(screen.queryByText('Set a model for reviews before they can run.')).not.toBeInTheDocument()
  })

  it('turns a review on or off with the version it was loaded with', async () => {
    const puts: ReviewInput[] = []
    useReviewMocks()
    server.use(
      http.put('/api/agent/reviews/:id', async ({ request }) => {
        puts.push((await request.json()) as ReviewInput)
        return HttpResponse.json({ ...REVIEW_MONTHLY_SECURITY, enabled: true, version: 2 })
      }),
    )
    renderApp('/settings/agent')
    const row = await rowOf('Monthly security')
    await userEvent.click(row.getByRole('switch', { name: 'Monthly security is on' }))
    await waitFor(() => expect(puts).toHaveLength(1))
    expect(puts[0]).toMatchObject({ name: 'Monthly security', type: 'security', enabled: true, version: 1, route_ids: [4], scope: [] })
    expect(puts[0].schedule).toEqual({ cadence: 'monthly', at_minute: 390, weekday: null, month_day: 15, timezone: 'UTC' })
  })

  it('run now confirms then reports busy', async () => {
    const runs: number[] = []
    useReviewMocks()
    server.use(
      http.post('/api/agent/reviews/:id/run', ({ params }) => {
        runs.push(Number(params.id))
        return runs.length === 1
          ? HttpResponse.json({ run_id: 77 }, { status: 202 })
          : HttpResponse.json({ error: { code: 'review_busy', message: 'A run is waiting.' } }, { status: 409 })
      }),
    )
    renderApp('/settings/agent')
    const row = await rowOf('Weekly cost')
    await userEvent.click(row.getByRole('button', { name: /^Run now/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Run this review now?')).toBeInTheDocument()
    expect(within(dialog).getByText('It uses the AI allowance and sends the result to its routes.')).toBeInTheDocument()
    expect(runs).toEqual([])
    await userEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(runs).toEqual([])

    await userEvent.click(row.getByRole('button', { name: /^Run now/ }))
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Run review' }))
    await waitFor(() => expect(runs).toEqual([1]))
    expect(await screen.findByText('The review has started. Its result goes to its routes.')).toBeInTheDocument()

    await userEvent.click(row.getByRole('button', { name: /^Run now/ }))
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Run review' }))
    expect(await screen.findByText('This review is already waiting or running.')).toBeInTheDocument()
  })

  it('shows what the server says when a run is refused for another reason', async () => {
    useReviewMocks()
    server.use(http.post('/api/agent/reviews/:id/run', () => HttpResponse.json({ error: { code: 'no_model', message: 'Set a model for reviews first.' } }, { status: 422 })))
    renderApp('/settings/agent')
    const row = await rowOf('Weekly cost')
    await userEvent.click(row.getByRole('button', { name: /^Run now/ }))
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Run review' }))
    expect(await screen.findByText('Set a model for reviews first.')).toBeInTheDocument()
  })

  it('delete confirms', async () => {
    const deleted: number[] = []
    useReviewMocks()
    server.use(
      http.delete('/api/agent/reviews/:id', ({ params }) => {
        deleted.push(Number(params.id))
        return new HttpResponse(null, { status: 204 })
      }),
    )
    renderApp('/settings/agent')
    const row = await rowOf('Weekly cost')
    await userEvent.click(row.getByRole('button', { name: /^Delete/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Delete this review?')).toBeInTheDocument()
    expect(within(dialog).getByText('Past results stay until they expire.')).toBeInTheDocument()
    await userEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(deleted).toEqual([])
    await userEvent.click(row.getByRole('button', { name: /^Delete/ }))
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Delete review' }))
    await waitFor(() => expect(deleted).toEqual([1]))
    expect(await screen.findByText('Review deleted.')).toBeInTheDocument()
  })

  it('offers no new review once the limit is reached', async () => {
    useReviewMocks({ settings: { ...REVIEW_SETTINGS, reviews: { ...REVIEW_SETTINGS.reviews, max_reviews: 2 } } })
    renderApp('/settings/agent')
    await rowOf('Weekly cost')
    expect(screen.getByRole('button', { name: 'Add review' })).toBeDisabled()
    expect(screen.getByText('You can have up to 2 reviews. Delete one to add another.')).toBeInTheDocument()
  })

  it('run now sends no body', async () => {
    const bodies: string[] = []
    useReviewMocks()
    server.use(
      http.post('/api/agent/reviews/:id/run', async ({ request }) => {
        bodies.push(await request.text())
        return HttpResponse.json({ run_id: 77 }, { status: 202 })
      }),
    )
    renderApp('/settings/agent')
    const row = await rowOf('Weekly cost')
    await userEvent.click(row.getByRole('button', { name: /^Run now/ }))
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Run review' }))
    await waitFor(() => expect(bodies).toEqual(['']))
  })

  it('test run confirms then starts with deliver false', async () => {
    const bodies: string[] = []
    useReviewMocks()
    server.use(
      http.post('/api/agent/reviews/:id/run', async ({ request }) => {
        bodies.push(await request.text())
        return bodies.length === 1
          ? HttpResponse.json({ run_id: 78 }, { status: 202 })
          : HttpResponse.json({ error: { code: 'review_busy', message: 'A run is waiting.' } }, { status: 409 })
      }),
    )
    renderApp('/settings/agent')
    const row = await rowOf('Weekly cost')
    await userEvent.click(row.getByRole('button', { name: /^Test run/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Run this review once without sending it? It uses the AI allowance. The result appears under Past results.')).toBeInTheDocument()
    await userEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(bodies).toEqual([])

    await userEvent.click(row.getByRole('button', { name: /^Test run/ }))
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Start test run' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(JSON.parse(bodies[0])).toEqual({ deliver: false })
    expect(await screen.findByText('The test run has started. Its result appears under Past results.')).toBeInTheDocument()

    await userEvent.click(row.getByRole('button', { name: /^Test run/ }))
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Start test run' }))
    expect(await screen.findByText('This review is already waiting or running.')).toBeInTheDocument()
  })

  it('custom row is read-only while the switch is off', async () => {
    const deleted: number[] = []
    useReviewMocks({ settings: REVIEW_SETTINGS, reviews: [REVIEW_WEEKLY_COST, REVIEW_CUSTOM] })
    server.use(
      http.delete('/api/agent/reviews/:id', ({ params }) => {
        deleted.push(Number(params.id))
        return new HttpResponse(null, { status: 204 })
      }),
    )
    renderApp('/settings/agent')
    const row = await rowOf('Team spend')
    expect(row.getByText('Custom reviews are turned off by configuration.')).toBeInTheDocument()
    expect(row.getByRole('button', { name: /^Delete/ })).toBeInTheDocument()
    for (const action of ['Run now', 'Test run', 'Edit']) expect(row.queryByRole('button', { name: new RegExp(`^${action}`) })).not.toBeInTheDocument()
    expect(row.queryByRole('switch')).not.toBeInTheDocument()
    // A built-in row is unchanged.
    const weekly = await rowOf('Weekly cost')
    expect(weekly.queryByText('Custom reviews are turned off by configuration.')).not.toBeInTheDocument()
    expect(weekly.getByRole('button', { name: /^Test run/ })).toBeInTheDocument()
    await userEvent.click(row.getByRole('button', { name: /^Delete/ }))
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Delete review' }))
    await waitFor(() => expect(deleted).toEqual([3]))
  })

  it('keeps a custom row fully usable while the switch is on', async () => {
    useReviewMocks({ settings: REVIEW_SETTINGS_CUSTOM, reviews: [REVIEW_CUSTOM] })
    renderApp('/settings/agent')
    const row = await rowOf('Team spend')
    expect(row.getByText('Custom')).toBeInTheDocument()
    expect(row.queryByText('Custom reviews are turned off by configuration.')).not.toBeInTheDocument()
    for (const action of ['Run now', 'Test run', 'Edit', 'Delete']) expect(row.getByRole('button', { name: new RegExp(`^${action}`) })).toBeInTheDocument()
  })
})
