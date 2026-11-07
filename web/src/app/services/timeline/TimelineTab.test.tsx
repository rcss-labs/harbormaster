import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { SERVICE_PAYMENTS } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'
import { LOG_ERRORS_NO_LOGS, LOG_ERRORS_OK, LOG_ERRORS_UNAVAILABLE, TIMELINE_EMPTY, TIMELINE_FAILING } from '../../../test/timelineFixtures'

const route = `/services/${SERVICE_PAYMENTS.id}/timeline`

const eventsTable = () => screen.getByRole('table')

describe('TimelineTab', () => {
  it('shows the five lane toggles, range options and events from both endpoints', async () => {
    renderApp(route)

    for (const lane of ['Changes', 'Task stops', 'Alerts', 'Log errors', 'Terminal']) {
      expect(await screen.findByRole('checkbox', { name: lane })).toBeChecked()
    }
    const ranges = within(screen.getByRole('radiogroup', { name: 'Range' }))
    for (const label of ['1 hour', '6 hours', '24 hours', '7 days']) expect(ranges.getByRole('radio', { name: label })).toBeInTheDocument()
    expect(ranges.getByRole('radio', { name: '1 hour' })).toBeChecked()

    const table = within(await screen.findByRole('table'))
    expect(await table.findByRole('button', { name: 'Deployed revision 6' })).toBeInTheDocument()
    expect(await table.findByRole('button', { name: '14 error lines in 5 minutes' })).toBeInTheDocument()
    expect(table.getByRole('button', { name: 'At least 120 error lines in 5 minutes' })).toBeInTheDocument()
    expect(screen.getByText('8 events in range')).toBeInTheDocument()
    expect(screen.getByText('Alerts: Shows alerts active now. Harbormaster does not keep cleared alerts.')).toBeInTheDocument()
  })

  it('changing the range refetches with that range and keeps it in the URL', async () => {
    const ranges: string[] = []
    server.use(
      http.get('/api/services/:id/timeline', ({ request }) => {
        const range = new URL(request.url).searchParams.get('range') ?? ''
        ranges.push(range)
        return HttpResponse.json({ ...TIMELINE_FAILING, range })
      }),
    )
    renderApp(route)
    const user = userEvent.setup()

    await user.click(await screen.findByRole('radio', { name: '6 hours' }))

    await waitFor(() => expect(ranges).toContain('6h'))
    expect(screen.getByTestId('search')).toHaveTextContent('range=6h')
    expect(screen.getByRole('radio', { name: '6 hours' })).toBeChecked()
  })

  it('turning a lane off removes its markers and rows; turning all off shows the hint', async () => {
    renderApp(route)
    const user = userEvent.setup()
    await within(await screen.findByRole('table')).findByRole('button', { name: 'Task 2f9c01aa stopped' })
    expect(screen.getAllByRole('button', { name: /Task 2f9c01aa stopped/ })).toHaveLength(2) // marker and table title

    await user.click(screen.getByRole('checkbox', { name: 'Task stops' }))

    expect(screen.queryAllByRole('button', { name: /Task 2f9c01aa stopped/ })).toHaveLength(0)
    expect(screen.getByText('5 events in range')).toBeInTheDocument()

    for (const lane of ['Changes', 'Alerts', 'Log errors', 'Terminal']) await user.click(screen.getByRole('checkbox', { name: lane }))

    expect(screen.getByText('Turn on at least one lane.')).toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  it('groups close markers with a count and selects the first item', async () => {
    const change = (id: string, at: string, title: string) => ({ ...TIMELINE_FAILING.events[0], id, at, title })
    server.use(
      http.get('/api/services/:id/timeline', () =>
        HttpResponse.json({
          ...TIMELINE_FAILING,
          incident: null,
          events: [change('op:1', '2026-11-07T11:30:00Z', 'Restarted'), change('op:2', '2026-11-07T11:31:00Z', 'Scaled to 4')],
        }),
      ),
    )
    renderApp(route)
    const user = userEvent.setup()

    const marker = await screen.findByRole('button', { name: /^\d\d:\d\d Restarted$/ })
    expect(marker).toHaveAttribute('title', marker.getAttribute('aria-label'))
    expect(marker).toHaveTextContent('2')
    expect(marker.className).not.toContain('sel')

    await user.click(marker)

    expect(screen.getByTestId('search')).toHaveTextContent('event=op%3A1')
    expect(screen.getByRole('button', { name: /^\d\d:\d\d Restarted$/ }).className).toContain('sel')
  })

  it('without Logs the lane is locked and no log-errors request is made', async () => {
    const logErrors = vi.fn()
    server.use(
      http.get('/api/services/:id', () => HttpResponse.json({ ...SERVICE_PAYMENTS, can: { ...SERVICE_PAYMENTS.can, logs: false } })),
      http.get('/api/services/:id/timeline/log-errors', () => {
        logErrors()
        return HttpResponse.json({})
      }),
    )
    renderApp(route)

    await within(await screen.findByRole('table')).findByRole('button', { name: 'Deployed revision 6' })
    const reason = `You need Logs on ${SERVICE_PAYMENTS.tag.name}`
    expect(screen.getByRole('checkbox', { name: 'Log errors' })).toBeDisabled()
    expect(within(screen.getByTestId('timeline-toggles')).getByTitle(reason)).toBeInTheDocument()
    expect(screen.getByText(reason)).toBeInTheDocument()
    expect(logErrors).not.toHaveBeenCalled()

    // A range change and a lane toggle are the moments a stray request could be made.
    const user = userEvent.setup()
    await user.click(screen.getByRole('radio', { name: '6 hours' }))
    await user.click(screen.getByRole('checkbox', { name: 'Terminal' }))
    await within(await screen.findByRole('table')).findByRole('button', { name: 'Deployed revision 6' })
    await new Promise((resolve) => setTimeout(resolve, 100))
    expect(logErrors).not.toHaveBeenCalled()
  })

  it('shows why the log lane is empty', async () => {
    server.use(http.get('/api/services/:id/timeline/log-errors', () => HttpResponse.json(LOG_ERRORS_NO_LOGS)))
    renderApp(route)

    expect(await screen.findByText(LOG_ERRORS_NO_LOGS.message)).toBeInTheDocument()
    expect(within(eventsTable()).getByRole('button', { name: 'Deployed revision 6' })).toBeInTheDocument()
  })

  it('when logs cannot load the lane offers Retry and other lanes stay', async () => {
    let calls = 0
    server.use(
      http.get('/api/services/:id/timeline/log-errors', () => {
        calls += 1
        return HttpResponse.json(LOG_ERRORS_UNAVAILABLE)
      }),
    )
    renderApp(route)
    const user = userEvent.setup()

    expect(await screen.findByText(LOG_ERRORS_UNAVAILABLE.message)).toBeInTheDocument()
    expect(within(eventsTable()).getByRole('button', { name: 'Deployed revision 6' })).toBeInTheDocument()
    await waitFor(() => expect(calls).toBe(1))

    await user.click(screen.getByRole('button', { name: 'Retry' }))

    await waitFor(() => expect(calls).toBe(2))
  })

  it('shows no events in this range', async () => {
    server.use(
      http.get('/api/services/:id/timeline', () => HttpResponse.json(TIMELINE_EMPTY)),
      http.get('/api/services/:id/timeline/log-errors', () => HttpResponse.json({ ...LOG_ERRORS_OK, buckets: [] })),
    )
    renderApp(route)

    expect(await screen.findByText('No events in this range.')).toBeInTheDocument()
    expect(screen.getByText('0 events in range')).toBeInTheDocument()
  })

  it('lists lane notes below the lanes and keeps a marker clamped to the range start clickable', async () => {
    const clamped = { ...TIMELINE_FAILING.events[4], at: TIMELINE_FAILING.from, evidence_at: '2026-11-06T09:00:00Z' }
    server.use(
      http.get('/api/services/:id/timeline', () =>
        HttpResponse.json({
          ...TIMELINE_FAILING,
          incident: null,
          events: [clamped],
          lanes: [
            { lane: 'change', truncated: true, note: '' },
            { lane: 'stop', truncated: false, note: 'Stops older than 7 days are not kept.' },
            { lane: 'alert', truncated: false, note: 'Shows alerts active now. Harbormaster does not keep cleared alerts.' },
          ],
        }),
      ),
      http.get('/api/services/:id/timeline/log-errors', () => HttpResponse.json({ ...LOG_ERRORS_OK, state: 'partial' })),
    )
    renderApp(route)
    const user = userEvent.setup()

    const notes = within(await screen.findByRole('list', { name: 'Lane notes' }))
    expect(notes.getByText('Alerts: Shows alerts active now. Harbormaster does not keep cleared alerts.')).toBeInTheDocument()
    expect(notes.getByText('Task stops: Stops older than 7 days are not kept.')).toBeInTheDocument()
    expect(notes.getByText('Changes: Showing the newest 500.')).toBeInTheDocument()
    expect(await notes.findByText('Log errors: Some periods could not be read.')).toBeInTheDocument()
    // Notes never sit inside a lane track, where they would cover markers.
    expect(screen.getByTestId('timeline-track')).not.toHaveTextContent('Shows alerts active now')

    await user.click(screen.getByRole('button', { name: /^\d\d:\d\d Alert active/ }))

    expect(screen.getByTestId('search')).toHaveTextContent('event=alert%3Aservice_failing')
  })

  it('falls back to 1 hour for an invalid range', async () => {
    renderApp(`${route}?range=3w`)

    expect(await screen.findByRole('radio', { name: '1 hour' })).toBeChecked()
  })

  it('renders an unknown event in the URL as nothing selected', async () => {
    renderApp(`${route}?event=nope`)

    expect(await screen.findByText('Select an event to see the details.')).toBeInTheDocument()
    expect(await screen.findByRole('table')).toBeInTheDocument()
  })

  it('makes no log-errors request for a range change while the lane is off', async () => {
    const logErrors = vi.fn()
    server.use(
      http.get('/api/services/:id/timeline/log-errors', () => {
        logErrors()
        return HttpResponse.json(LOG_ERRORS_OK)
      }),
    )
    renderApp(route)
    const user = userEvent.setup()
    await within(await screen.findByRole('table')).findByRole('button', { name: '14 error lines in 5 minutes' })
    await user.click(screen.getByRole('checkbox', { name: 'Log errors' }))
    logErrors.mockClear()

    await user.click(screen.getByRole('radio', { name: '6 hours' }))
    await within(await screen.findByRole('table')).findByRole('button', { name: 'Deployed revision 6' })
    await new Promise((resolve) => setTimeout(resolve, 100))

    expect(logErrors).not.toHaveBeenCalled()
  })

  it('shows the Logs lock reason beside the hint when every other lane is off', async () => {
    server.use(http.get('/api/services/:id', () => HttpResponse.json({ ...SERVICE_PAYMENTS, can: { ...SERVICE_PAYMENTS.can, logs: false } })))
    renderApp(route)
    const user = userEvent.setup()
    await within(await screen.findByRole('table')).findByRole('button', { name: 'Deployed revision 6' })

    for (const lane of ['Changes', 'Task stops', 'Alerts', 'Terminal']) await user.click(screen.getByRole('checkbox', { name: lane }))

    expect(screen.getByText('Turn on at least one lane.')).toBeInTheDocument()
    expect(screen.getByText(`You need Logs on ${SERVICE_PAYMENTS.tag.name}`)).toBeInTheDocument()
  })
})
