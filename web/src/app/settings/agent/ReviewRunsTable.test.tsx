import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'
import { reviewRun, useReviewMocks } from '../../../test/reviewSettingsFixtures'

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})

const SLOW = { timeout: 4000 }

describe('ReviewRunsTable', () => {
  it('lists past results and loads more', async () => {
    const urls: string[] = []
    useReviewMocks()
    server.use(
      http.get('/api/agent/reviews/runs', ({ request }) => {
        urls.push(new URL(request.url).search)
        const cursor = new URL(request.url).searchParams.get('cursor')
        return HttpResponse.json(
          cursor
            ? { items: [reviewRun(31, { period_label: 'Week 45, 2026', state: 'skipped', reason: 'budget', cost_usd: '', attempts: 0, requested_at: '2026-11-09T07:00:00Z' })], next_cursor: '' }
            : { items: [reviewRun(41), reviewRun(40, { period_label: 'Week 46, 2026', state: 'failed', reason: 'no_result', attempts: 2, cost_usd: '0.62', manual: true })], next_cursor: 'c1' },
        )
      }),
    )
    renderApp('/settings/agent')
    const panel = within(await screen.findByRole('region', { name: 'Reviews' }, SLOW))
    const row = (await panel.findByText('Weekly cost', undefined, SLOW)).closest('tr') as HTMLElement
    await userEvent.click(within(row).getByRole('button', { name: /^Edit/ }))
    const table = within(await screen.findByRole('region', { name: 'Past results of Weekly cost' }, SLOW))
    expect(table.getByRole('link', { name: /Week 47, 2026/ })).toHaveAttribute('href', '/reviews/runs/41')
    expect(table.getByText('Done')).toBeInTheDocument()
    expect(table.getByText('$0.31')).toBeInTheDocument()
    expect(table.getByText('Failed')).toBeInTheDocument()
    expect(table.getByText('no answer')).toBeInTheDocument()
    expect(table.getByText('$0.62')).toBeInTheDocument()
    expect(urls[0]).toContain('review_id=1')
    expect(table.queryByText('Skipped')).not.toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'Load more' }))
    expect(await table.findByText('Skipped', undefined, SLOW)).toBeInTheDocument()
    expect(table.getByText('allowance used up')).toBeInTheDocument()
    expect(table.getByText('n/a')).toBeInTheDocument()
    expect(urls[1]).toContain('cursor=c1')
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument()
  })

  it('says so when a review has no results yet', async () => {
    useReviewMocks()
    renderApp('/settings/agent')
    const panel = within(await screen.findByRole('region', { name: 'Reviews' }, SLOW))
    const row = (await panel.findByText('Monthly security', undefined, SLOW)).closest('tr') as HTMLElement
    await userEvent.click(within(row).getByRole('button', { name: /^Edit/ }))
    expect(await screen.findByText('No results yet.', undefined, SLOW)).toBeInTheDocument()
  })

  const openPastResults = async () => {
    renderApp('/settings/agent')
    const panel = within(await screen.findByRole('region', { name: 'Reviews' }, SLOW))
    const row = (await panel.findByText('Weekly cost', undefined, SLOW)).closest('tr') as HTMLElement
    await userEvent.click(within(row).getByRole('button', { name: /^Edit/ }))
    return within(await screen.findByRole('region', { name: 'Past results of Weekly cost' }, SLOW))
  }

  it('marks test runs', async () => {
    useReviewMocks({ runs: { items: [reviewRun(51, { period_label: 'Test on 25 Nov 2026, 10:00 UTC', manual: true, test: true }), reviewRun(50, { period_label: 'Week 47, 2026' })], next_cursor: '' } })
    const table = await openPastResults()
    const test = (await table.findByRole('link', { name: /Test on 25 Nov 2026/ }, SLOW)).closest('tr') as HTMLElement
    expect(within(test).getByText('Test')).toBeInTheDocument()
    expect(within(test).queryByText('run now')).not.toBeInTheDocument()
    const normal = table.getByRole('link', { name: /Week 47, 2026/ }).closest('tr') as HTMLElement
    expect(within(normal).queryByText('Test')).not.toBeInTheDocument()
  })

  it('shows the instructions used as text', async () => {
    const markup = 'Look at <b>spend</b> and [this](https://example.invalid) <img src=x onerror=alert(1)>'
    useReviewMocks({ runs: { items: [reviewRun(52, { review_id: 1, instructions: markup }), reviewRun(53, { period_label: 'Week 46, 2026' })], next_cursor: '' } })
    const table = await openPastResults()
    expect(await table.findByText('Instructions used', undefined, SLOW)).toBeInTheDocument()
    expect(table.getAllByText('Instructions used')).toHaveLength(1)
    const shown = table.getByText(markup)
    expect(shown.tagName).toBe('PRE')
    expect(shown.querySelector('b, a, img')).toBeNull()
  })
})
