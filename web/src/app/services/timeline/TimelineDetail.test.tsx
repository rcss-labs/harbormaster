import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { delay, http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { ALL_TASKS, SERVICE_PAYMENTS } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'
import { TIMELINE_FAILING } from '../../../test/timelineFixtures'

const route = `/services/${SERVICE_PAYMENTS.id}/timeline`

// The panel is replaced when a selection appears, so every query looks the region up again.
const region = () => within(screen.getByRole('region', { name: 'Details' }))
const details = async () => {
  await screen.findByRole('region', { name: 'Details' })
  return {
    findByText: (text: string) => waitFor(() => region().getByText(text)),
    findByRole: (...args: Parameters<ReturnType<typeof region>['getByRole']>) => waitFor(() => region().getByRole(...args)),
    getByText: (text: string | RegExp) => region().getByText(text),
    getByRole: (...args: Parameters<ReturnType<typeof region>['getByRole']>) => region().getByRole(...args),
    queryByText: (text: string) => region().queryByText(text),
  }
}

const definition = (revision: number, image: string, memory: string) => ({
  revision,
  arn: `arn:aws:ecs:us-east-1:111122223333:task-definition/image-resizer:${revision}`,
  definition: { family: 'image-resizer', cpu: '512', memory, containerDefinitions: [{ name: 'app', image }] },
  registered_at: '2026-11-07T11:00:00Z',
  registered_by: 'sam',
})

function serveRevisions() {
  server.use(
    http.get('/api/services/:id/revisions/:revision', ({ params }) =>
      HttpResponse.json(Number(params.revision) === 5 ? definition(5, 'resizer:1.4', '1024') : definition(6, 'resizer:1.5', '512')),
    ),
  )
}

function serveTimeline(events: unknown[], incident: unknown = null) {
  server.use(http.get('/api/services/:id/timeline', () => HttpResponse.json({ ...TIMELINE_FAILING, incident, events })))
}

const stopEvent = TIMELINE_FAILING.events[1]
const scaleEvent = { ...TIMELINE_FAILING.events[0], id: 'op:4200', op: 'scale', title: 'Scaled to 4', from_revision: null, to_revision: null, detail: '', diff: [{ label: 'desired count', from: '2', to: '4' }] }

describe('TimelineDetail', () => {
  it('selects the trigger by default, with the pill, time, actor and detail', async () => {
    serveRevisions()
    renderApp(route)

    const panel = await details()
    expect(await panel.findByText('Deployed revision 6')).toBeInTheDocument()
    expect(panel.getByText('Likely trigger')).toBeInTheDocument()
    expect(panel.getByText(/sam/)).toBeInTheDocument()
    expect(panel.getByText('Raise the image resizer batch size.')).toBeInTheDocument()
  })

  it('honours ?event= and does not show the trigger pill on other events', async () => {
    renderApp(`${route}?event=${encodeURIComponent(stopEvent.id)}`)

    const panel = await details()
    expect(await panel.findByText('Task 2f9c01aa stopped')).toBeInTheDocument()
    expect(panel.queryByText('Likely trigger')).not.toBeInTheDocument()
  })

  it('turns on the lane of the selected event when it was switched off before the timeline loaded', async () => {
    server.use(
      http.get('/api/services/:id/timeline', async () => {
        await delay(150)
        return HttpResponse.json(TIMELINE_FAILING)
      }),
    )
    renderApp(`${route}?event=op%3A4102`)
    const user = userEvent.setup()

    await user.click(await screen.findByRole('checkbox', { name: 'Changes' }))

    expect(await (await details()).findByText('Raise the image resizer batch size.')).toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: 'Changes' })).toBeChecked()
  })

  it('shows the revision diff for a change', async () => {
    serveRevisions()
    renderApp(route)

    const panel = await details()
    expect(await panel.findByText('resizer:1.4')).toBeInTheDocument()
    expect(panel.getByText('resizer:1.5')).toBeInTheDocument()
    expect(panel.getByText('image')).toBeInTheDocument()
    expect(panel.getByText('memory')).toBeInTheDocument()
    expect(panel.getByText('1024')).toBeInTheDocument()
    expect(panel.getByText('512')).toBeInTheDocument()
  })

  it('shows the server diff for a desired count change', async () => {
    serveTimeline([scaleEvent])
    renderApp(`${route}?event=op%3A4200`)

    const panel = await details()
    expect(await panel.findByText('desired count')).toBeInTheDocument()
    expect(panel.getByText('2')).toBeInTheDocument()
    expect(panel.getByText('4')).toBeInTheDocument()
  })

  it('says when the diff cannot be loaded and links to the Task definition tab', async () => {
    server.use(http.get('/api/services/:id/revisions/:revision', () => HttpResponse.json({ error: { code: 'internal', message: 'boom' } }, { status: 500 })))
    renderApp(route)

    const panel = await details()
    expect(await panel.findByText('The diff could not be loaded.')).toBeInTheDocument()
    expect(panel.getByRole('link', { name: 'Task definition' })).toHaveAttribute('href', `/services/${SERVICE_PAYMENTS.id}/task-definition`)
  })

  it('shows the time the evidence was seen when an alert was clamped to the range start', async () => {
    const alert = { ...TIMELINE_FAILING.events[4], id: 'alert:old', at: TIMELINE_FAILING.from, evidence_at: '2026-11-06T09:00:00Z' }
    serveTimeline([alert])
    renderApp(`${route}?event=alert%3Aold`)

    const evidence = new Date('2026-11-06T09:00:00Z')
    const pad = (n: number) => String(n).padStart(2, '0')
    const local = `Evidence from ${evidence.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })} ${pad(evidence.getHours())}:${pad(evidence.getMinutes())}`
    expect(await (await details()).findByText(local)).toBeInTheDocument()
  })

  it('links to logs one minute after the event', async () => {
    renderApp(route)

    const link = await (await details()).findByRole('link', { name: 'Open logs at this time' })
    expect(link.getAttribute('href')).toBe(`/services/${SERVICE_PAYMENTS.id}/logs?before=${encodeURIComponent('2026-11-07T11:49:00.000Z')}`)
  })

  it('links a log error event to its bucket end', async () => {
    renderApp(route)
    const user = userEvent.setup()

    await user.click(await within(await screen.findByRole('table')).findByRole('button', { name: '14 error lines in 5 minutes' }))

    const link = await (await details()).findByRole('link', { name: 'Open logs at this time' })
    expect(link.getAttribute('href')).toBe(`/services/${SERVICE_PAYMENTS.id}/logs?before=${encodeURIComponent('2026-11-07T11:55:00.000Z')}`)
  })

  it('locks the logs link without Logs and shows no log-derived text', async () => {
    server.use(http.get('/api/services/:id', () => HttpResponse.json({ ...SERVICE_PAYMENTS, can: { ...SERVICE_PAYMENTS.can, logs: false } })))
    renderApp(route)

    const panel = await details()
    const reason = `You need Logs on ${SERVICE_PAYMENTS.tag.name}`
    const locked = await panel.findByText('Open logs at this time')
    expect(locked.closest('a')).toBeNull()
    expect(locked.closest('[aria-disabled="true"]')).toHaveAttribute('title', reason)
    expect(screen.queryByText(/error lines in/)).not.toBeInTheDocument()
  })

  it('Open task opens the drawer for an old stopped task', async () => {
    const stopped = ALL_TASKS.find((task) => task.status === 'STOPPED')!
    server.use(http.get('/api/services/:id/timeline/tasks/:taskId', () => HttpResponse.json(stopped)))
    renderApp(`${route}?event=${encodeURIComponent(stopEvent.id)}`)
    const user = userEvent.setup()

    await user.click(await (await details()).findByRole('button', { name: 'Open task' }))

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(stopped.id)).toBeInTheDocument()
  })

  it('says the task is gone on a 404 and offers Open task only for stops', async () => {
    server.use(http.get('/api/services/:id/timeline/tasks/:taskId', () => HttpResponse.json({ error: { code: 'not_found', message: 'Not found.' } }, { status: 404 })))
    renderApp(`${route}?event=${encodeURIComponent(stopEvent.id)}`)
    const user = userEvent.setup()

    await user.click(await (await details()).findByRole('button', { name: 'Open task' }))

    expect(await screen.findByText('This task is no longer stored.')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await user.click(within(screen.getByRole('table')).getByRole('button', { name: 'Deployed revision 6' }))
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Open task' })).not.toBeInTheDocument())
  })
})
