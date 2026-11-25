import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import type { ReviewDTO, ReviewInput } from '../../../api/agent'
import { CORE_SERVICES_CLUSTER } from '../../../test/notificationsFixtures'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'
import { PREVIEW_SMALL, REVIEW_SETTINGS, REVIEW_WEEKLY_COST, useReviewMocks } from '../../../test/reviewFixtures'

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})

const SLOW = { timeout: 4000 }
const CORE_ROUTE = 'core-services to #core-alerts'

const openNew = async () => {
  await userEvent.click(await screen.findByRole('button', { name: 'Add review' }, SLOW))
  return screen.findByLabelText('Review name', undefined, SLOW)
}
const openEdit = async (name: string) => {
  const panel = within(await screen.findByRole('region', { name: 'Reviews' }, SLOW))
  const row = (await panel.findByText(name, undefined, SLOW)).closest('tr') as HTMLElement
  await userEvent.click(within(row).getByRole('button', { name: /^Edit/ }))
  await screen.findByLabelText('Review name', undefined, SLOW)
}
const chooseRoute = (name: string) => userEvent.click(within(screen.getByRole('group', { name: 'Routes' })).getByRole('checkbox', { name }))
const save = () => userEvent.click(screen.getByRole('button', { name: 'Save review' }))
/** Saves the body into the list the way the server would, and answers with the stored review. */
const store = (list: ReviewDTO[], body: ReviewInput, id: string | readonly string[] | undefined) => {
  const index = list.findIndex((review) => String(review.id) === id)
  list[index] = { ...list[index], name: body.name, enabled: body.enabled, schedule: body.schedule, version: (body.version ?? 0) + 1 }
  return HttpResponse.json(list[index])
}

describe('ReviewEditor', () => {
  it('saves a weekly review', async () => {
    const posts: ReviewInput[] = []
    useReviewMocks()
    server.use(
      http.post('/api/agent/reviews', async ({ request }) => {
        posts.push((await request.json()) as ReviewInput)
        return HttpResponse.json({ ...REVIEW_WEEKLY_COST, id: 9, name: 'Friday reliability', version: 1 }, { status: 201 })
      }),
    )
    renderApp('/settings/agent')
    const name = await openNew()
    expect(screen.getByRole('button', { name: 'Save review' })).toBeDisabled()
    await userEvent.clear(name)
    await userEvent.type(name, 'Friday reliability')
    await userEvent.click(screen.getByRole('radio', { name: /Reliability/ }))
    await userEvent.selectOptions(screen.getByLabelText('Cadence'), 'weekly')
    await userEvent.selectOptions(screen.getByLabelText('Weekday'), 'Friday')
    fireEvent.change(screen.getByLabelText('Time'), { target: { value: '07:30' } })
    await userEvent.selectOptions(screen.getByLabelText('Time zone'), 'Europe/Berlin')
    const box = screen.getByRole('combobox', { name: 'Add to scope' })
    await userEvent.type(box, 'core-services')
    await userEvent.click(await screen.findByRole('option', { name: /core-services/ }))
    await chooseRoute(CORE_ROUTE)
    await userEvent.click(screen.getByLabelText('Review is on'))
    expect(screen.getByText('Unsaved changes')).toBeInTheDocument()
    await save()
    await waitFor(() => expect(posts).toHaveLength(1))
    expect(posts[0]).toEqual({
      name: 'Friday reliability',
      type: 'reliability',
      enabled: true,
      schedule: { cadence: 'weekly', at_minute: 450, weekday: 5, month_day: null, timezone: 'Europe/Berlin' },
      scope: [{ kind: 'cluster', cluster_id: CORE_SERVICES_CLUSTER.cluster_id }],
      route_ids: [2],
    })
    expect(await screen.findByText('Saved "Friday reliability".')).toBeInTheDocument()
  })

  it('offers only tags, clusters and accounts for the scope, with no threshold', async () => {
    useReviewMocks()
    renderApp('/settings/agent')
    await openNew()
    const box = screen.getByRole('combobox', { name: 'Add to scope' })
    await userEvent.type(box, 'payments')
    expect(screen.queryByRole('option', { name: /payments-api/ })).not.toBeInTheDocument()
    await userEvent.clear(box)
    await userEvent.type(box, 'env=prod')
    expect(screen.queryByRole('option', { name: /AWS tag/ })).not.toBeInTheDocument()
    await userEvent.clear(box)
    await userEvent.type(box, 'Prod')
    await userEvent.click(await screen.findByRole('option', { name: /Prod/ }))
    expect(screen.queryByRole('spinbutton', { name: /Failing services needed/ })).not.toBeInTheDocument()
  })

  it('requires a route', async () => {
    useReviewMocks()
    renderApp('/settings/agent')
    const name = await openNew()
    await userEvent.type(name, ' weekly')
    expect(screen.getByRole('button', { name: 'Save review' })).toBeDisabled()
    expect(screen.getByText('Choose at least one route.')).toBeInTheDocument()
    await chooseRoute(CORE_ROUTE)
    expect(screen.getByRole('button', { name: 'Save review' })).toBeEnabled()
    expect(screen.queryByText('Choose at least one route.')).not.toBeInTheDocument()
  })

  it('does not offer PagerDuty routes', async () => {
    useReviewMocks()
    renderApp('/settings/agent')
    await openNew()
    const routes = within(screen.getByRole('group', { name: 'Routes' }))
    expect(await routes.findByRole('checkbox', { name: CORE_ROUTE })).toBeInTheDocument()
    expect(routes.queryByRole('checkbox', { name: 'Prod incidents to on-call' })).not.toBeInTheDocument()
  })

  it('shows the unseen services line', async () => {
    useReviewMocks({ preview: { ...PREVIEW_SMALL, services_total: 40, services_unseen: 7 } })
    renderApp('/settings/agent')
    await openNew()
    await chooseRoute(CORE_ROUTE)
    expect(await screen.findByText('40 services in scope.', undefined, SLOW)).toBeInTheDocument()
    expect(screen.getByText('The agent cannot see 7 of them. Change its access under Agent identity.')).toBeInTheDocument()
    expect(screen.queryByText(/Split this review/)).not.toBeInTheDocument()
  })

  it('shows the split advice above 150 services', async () => {
    useReviewMocks({ preview: { ...PREVIEW_SMALL, services_total: 151 } })
    renderApp('/settings/agent')
    await openNew()
    await chooseRoute(CORE_ROUTE)
    expect(await screen.findByText('151 services in scope.', undefined, SLOW)).toBeInTheDocument()
    expect(screen.getByText('One run reads the largest problems first. Split this review by account or tag for more depth.')).toBeInTheDocument()
    expect(screen.queryByText(/cannot see/)).not.toBeInTheDocument()
  })

  it('shows no split advice at exactly 150 services', async () => {
    useReviewMocks({ preview: { ...PREVIEW_SMALL, services_total: 150 } })
    renderApp('/settings/agent')
    await openNew()
    await chooseRoute(CORE_ROUTE)
    expect(await screen.findByText('150 services in scope.', undefined, SLOW)).toBeInTheDocument()
    expect(screen.queryByText(/Split this review/)).not.toBeInTheDocument()
  })

  it('shows the worst case and the next run in the review zone', async () => {
    const previews: ReviewInput[] = []
    useReviewMocks({ preview: { ...PREVIEW_SMALL, next_run_at: '2026-12-07T07:00:00Z', worst_case_month_usd: '12.50' }, previews })
    renderApp('/settings/agent')
    await openNew()
    await userEvent.selectOptions(screen.getByLabelText('Time zone'), 'Europe/Berlin')
    await chooseRoute(CORE_ROUTE)
    expect(await screen.findByText('Worst case: $12.50 a month at the current model.', undefined, SLOW)).toBeInTheDocument()
    expect(screen.getByText(/^Next run: .*08:00 Europe\/Berlin\.$/)).toBeInTheDocument()
    await waitFor(() => expect(previews.length).toBeGreaterThan(0))
    expect(previews.at(-1)).not.toHaveProperty('version')
    expect(previews.at(-1)?.schedule.timezone).toBe('Europe/Berlin')
  })

  it('says nothing about money or the next run when the preview has none', async () => {
    useReviewMocks({ preview: { ...PREVIEW_SMALL, next_run_at: null, worst_case_month_usd: null } })
    renderApp('/settings/agent')
    await openNew()
    await chooseRoute(CORE_ROUTE)
    expect(await screen.findByText('12 services in scope.', undefined, SLOW)).toBeInTheDocument()
    expect(screen.queryByText(/Worst case:/)).not.toBeInTheDocument()
    expect(screen.queryByText(/Next run:/)).not.toBeInTheDocument()
  })

  it('disables an unsupported type', async () => {
    useReviewMocks()
    renderApp('/settings/agent')
    await openNew()
    const capacity = screen.getByRole('radio', { name: /Capacity/ })
    expect(capacity).toBeDisabled()
    expect(screen.getAllByText('The agent service does not offer this type yet.')).toHaveLength(1)
    const cost = screen.getByRole('radio', { name: /Cost/ })
    expect(cost).toBeEnabled()
    expect(cost).toBeChecked()
    expect(screen.getByText(/^Reads spend/)).toBeInTheDocument()
  })

  it('weekday only for weekly and day only for monthly', async () => {
    const puts: ReviewInput[] = []
    const list: ReviewDTO[] = [{ ...REVIEW_WEEKLY_COST }]
    useReviewMocks({ reviews: list })
    server.use(
      http.put('/api/agent/reviews/:id', async ({ request, params }) => {
        const body = (await request.json()) as ReviewInput
        puts.push(body)
        return store(list, body, params.id)
      }),
    )
    renderApp('/settings/agent')
    await openEdit('Weekly cost')
    expect(screen.getByLabelText('Weekday')).toHaveValue('1')
    expect(screen.queryByLabelText('Day of month')).not.toBeInTheDocument()

    await userEvent.selectOptions(screen.getByLabelText('Cadence'), 'daily')
    expect(screen.queryByLabelText('Weekday')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Day of month')).not.toBeInTheDocument()
    await save()
    await waitFor(() => expect(puts).toHaveLength(1))
    expect(puts[0].schedule).toEqual({ cadence: 'daily', at_minute: 480, weekday: null, month_day: null, timezone: 'Europe/Berlin' })
    expect(puts[0].version).toBe(4)

    await userEvent.selectOptions(screen.getByLabelText('Cadence'), 'monthly')
    expect(screen.queryByLabelText('Weekday')).not.toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Day of month'), { target: { value: '20' } })
    await save()
    await waitFor(() => expect(puts).toHaveLength(2))
    expect(puts[1].schedule).toEqual({ cadence: 'monthly', at_minute: 480, weekday: null, month_day: 20, timezone: 'Europe/Berlin' })

    await userEvent.selectOptions(screen.getByLabelText('Cadence'), 'weekly')
    expect(screen.queryByLabelText('Day of month')).not.toBeInTheDocument()
    await userEvent.selectOptions(screen.getByLabelText('Weekday'), 'Wednesday')
    await save()
    await waitFor(() => expect(puts).toHaveLength(3))
    expect(puts[2].schedule).toEqual({ cadence: 'weekly', at_minute: 480, weekday: 3, month_day: null, timezone: 'Europe/Berlin' })
    expect(puts[2].version).toBe(6)
  })

  it('refuses a day of the month above 28', async () => {
    useReviewMocks()
    renderApp('/settings/agent')
    await openEdit('Weekly cost')
    await userEvent.selectOptions(screen.getByLabelText('Cadence'), 'monthly')
    fireEvent.change(screen.getByLabelText('Day of month'), { target: { value: '29' } })
    expect(screen.getByText('Choose a day from 1 to 28.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save review' })).toBeDisabled()
  })

  it('shows the server refusal next to the form', async () => {
    useReviewMocks()
    server.use(http.post('/api/agent/reviews', () => HttpResponse.json({ error: { code: 'review_limit', message: 'You can have up to 20 reviews.' } }, { status: 422 })))
    renderApp('/settings/agent')
    await openNew()
    await chooseRoute(CORE_ROUTE)
    await save()
    expect(await screen.findByText('You can have up to 20 reviews.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save review' })).toBeEnabled()
  })

  it('keeps what the admin changed and loads the rest after a conflict', async () => {
    const puts: ReviewInput[] = []
    const list: ReviewDTO[] = [{ ...REVIEW_WEEKLY_COST }]
    useReviewMocks({ reviews: list })
    server.use(
      http.put('/api/agent/reviews/:id', async ({ request }) => {
        const body = (await request.json()) as ReviewInput
        puts.push(body)
        if (puts.length === 1) {
          // Someone else saved first: a new name for the other fields to follow, and a later time.
          list[0] = { ...list[0], name: 'Theirs', schedule: { ...list[0].schedule, at_minute: 540 }, version: 5 }
          return HttpResponse.json({ error: { code: 'review_changed', message: 'This review was changed by someone else.' } }, { status: 409 })
        }
        list[0] = { ...list[0], name: body.name, version: 6 }
        return HttpResponse.json(list[0])
      }),
    )
    renderApp('/settings/agent')
    await openEdit('Weekly cost')
    const enabled = screen.getByLabelText('Review is on')
    await userEvent.click(enabled)
    await save()
    expect(await screen.findByText(/This review was changed by someone else\./, undefined, SLOW)).toBeInTheDocument()
    // The switch stays as the admin set it; the time follows the stored review.
    await waitFor(() => expect(screen.getByLabelText('Time')).toHaveValue('09:00'))
    expect(screen.getByLabelText('Review is on')).not.toBeChecked()
    expect(screen.getByLabelText('Review name')).toHaveValue('Theirs')
    await save()
    await waitFor(() => expect(puts).toHaveLength(2))
    expect(puts[1]).toMatchObject({ name: 'Theirs', enabled: false, version: 5 })
    expect(puts[1].schedule.at_minute).toBe(540)
  })

  it('discards the draft back to the saved review', async () => {
    useReviewMocks({ settings: REVIEW_SETTINGS })
    renderApp('/settings/agent')
    await openEdit('Weekly cost')
    const name = screen.getByLabelText('Review name')
    await userEvent.type(name, ' x')
    expect(screen.getByText('Unsaved changes')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Discard changes' }))
    expect(name).toHaveValue('Weekly cost')
    expect(screen.queryByText('Unsaved changes')).not.toBeInTheDocument()
  })
})
