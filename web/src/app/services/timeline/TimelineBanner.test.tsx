import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { delay, http, HttpResponse } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { SERVICE_PAYMENTS } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'
import { TIMELINE_FAILING } from '../../../test/timelineFixtures'

const route = `/services/${SERVICE_PAYMENTS.id}/timeline`
const incident = TIMELINE_FAILING.incident

const definition = (revision: number, memory: string) => ({
  revision,
  arn: `arn:aws:ecs:us-east-1:111122223333:task-definition/image-resizer:${revision}`,
  definition: { family: 'image-resizer', cpu: '512', memory, containerDefinitions: [{ name: 'app', image: 'resizer:1.4' }] },
  registered_at: '2026-11-07T11:00:00Z',
  registered_by: 'sam',
})

function serveRevisions() {
  server.use(http.get('/api/services/:id/revisions/:revision', ({ params }) => HttpResponse.json(Number(params.revision) === 5 ? definition(5, '1024') : definition(6, '512'))))
}

function serveIncident(fields: Record<string, unknown> | null, events: unknown[] = TIMELINE_FAILING.events) {
  server.use(http.get('/api/services/:id/timeline', () => HttpResponse.json({ ...TIMELINE_FAILING, events, incident: fields === null ? null : { ...incident, ...fields } })))
}

const banner = async (name: RegExp) => (await screen.findByText(name)).closest('[data-hm-banner]') as HTMLElement

describe('TimelineBanner', () => {
  it('names the change, the delay and the top reason', async () => {
    serveRevisions()
    renderApp(route)

    const warning = within(await banner(/Tasks began failing 3 minutes after revision 6\./))
    expect(await warning.findByText(/It lowered memory from 1024 to 512 MiB\./)).toBeInTheDocument()
    expect(warning.getByText(/3 of 3 failures since then: Out of memory\./)).toBeInTheDocument()
    expect(warning.getByRole('button', { name: 'Show the change' })).toBeInTheDocument()
    expect(screen.getByText('Harbormaster sees its own changes and new deployments, not other changes made directly in AWS.')).toBeInTheDocument()
  })

  it('says so when no change preceded the failure', async () => {
    serveIncident({ trigger: null, rollback_revision: null })
    renderApp(route)

    const warning = within(await banner(/Tasks began failing at \d\d:\d\d\./))
    expect(warning.getByText(/No change was recorded in the 24 hours before\./)).toBeInTheDocument()
    expect(warning.queryByRole('button', { name: 'Show the change' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Roll back/ })).not.toBeInTheDocument()
  })

  it('shows the rollback state instead of the warning', async () => {
    const rolledBack = { ...TIMELINE_FAILING.events[0], id: 'op:4300', op: 'rollback', title: 'Rolled back to revision 5', actor: 'omer', from_revision: 6, to_revision: 5 }
    serveIncident({ rolled_back: rolledBack })
    renderApp(route)

    const ok = within(await banner(/Rollback to revision 5 started by omer, \d\d:\d\d\./))
    expect(ok.getByText(/The change appears on the timeline with its reason\./)).toBeInTheDocument()
    expect(screen.queryByText(/Tasks began failing/)).not.toBeInTheDocument()
  })

  it('when the trigger is unknown, says so and offers neither the change nor a rollback', async () => {
    serveIncident({ trigger: null, trigger_unknown: true, rollback_revision: null })
    renderApp(route)

    const warning = within(await banner(/Tasks have been failing for a long time\./))
    expect(warning.getByText(/Harbormaster holds too many recent failures to tell what started it\./)).toBeInTheDocument()
    expect(warning.getByText(/Check the Deploy tab for revision history\./)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Show the change' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Roll back/ })).not.toBeInTheDocument()
  })

  it('has no banner when the last failure is before the range', async () => {
    serveIncident({ last_failure_at: '2026-11-07T10:30:00Z' })
    renderApp(route)

    await within(await screen.findByRole('table')).findByRole('button', { name: 'Deployed revision 6' })
    expect(screen.queryByText(/Tasks began failing/)).not.toBeInTheDocument()
  })
})

describe('Show the change', () => {
  const OLD_TRIGGER = { ...TIMELINE_FAILING.events[0], id: 'op:old', at: '2026-11-07T07:00:00Z', title: 'Restarted the service', from_revision: null, to_revision: null, diff: [] }

  function serveOldTrigger(trigger: typeof OLD_TRIGGER) {
    server.use(
      http.get('/api/services/:id/timeline', ({ request }) => {
        const range = new URL(request.url).searchParams.get('range') ?? '1h'
        const inRange = range !== '1h' && Date.parse(trigger.at) >= Date.now() - (range === '6h' ? 6 : 24) * 3_600_000
        return HttpResponse.json({
          ...TIMELINE_FAILING,
          range,
          events: inRange ? [trigger] : [],
          incident: { ...incident, trigger, rollback_revision: null },
        })
      }),
    )
  }

  it('widens the range and turns the lane back on', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    try {
      vi.setSystemTime(new Date('2026-11-07T12:00:00Z'))
      serveOldTrigger(OLD_TRIGGER)
      renderApp(route)
      const user = userEvent.setup()
      await user.click(await screen.findByRole('checkbox', { name: 'Changes' }))
      expect(screen.getByRole('checkbox', { name: 'Changes' })).not.toBeChecked()

      await user.click(await screen.findByRole('button', { name: 'Show the change' }))

      await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent('range=6h'))
      expect(screen.getByTestId('search')).toHaveTextContent('event=op%3Aold')
      expect(screen.getByRole('checkbox', { name: 'Changes' })).toBeChecked()
      expect(await within(screen.getByRole('region', { name: 'Details' })).findByText('Restarted the service')).toBeInTheDocument()
    } finally {
      vi.useRealTimers()
    }
  })

  it('keeps the range and still shows a trigger older than 7 days', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    try {
      vi.setSystemTime(new Date('2026-11-07T12:00:00Z'))
      serveOldTrigger({ ...OLD_TRIGGER, at: '2026-10-25T07:00:00Z' })
      renderApp(route)
      const user = userEvent.setup()

      await user.click(await screen.findByRole('button', { name: 'Show the change' }))

      expect(screen.getByTestId('search')).not.toHaveTextContent('range=')
      expect(await within(screen.getByRole('region', { name: 'Details' })).findByText('Restarted the service')).toBeInTheDocument()
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('Roll back', () => {
  const rollbackButton = () => screen.findByRole('button', { name: 'Roll back to revision 5' })

  it('navigates to the Deploy tab review and makes no request of its own', async () => {
    const posts: string[] = []
    const record = ({ request }: { request: Request }) => {
      if (request.method !== 'GET') posts.push(`${request.method} ${new URL(request.url).pathname}`)
    }
    server.events.on('request:start', record)
    // Keeps the Deploy tab from resolving the revision, so it does not consume ?review=1 before the assertion.
    server.use(http.get('/api/services/:id/revisions/:revision', async () => delay('infinite')))
    try {
      renderApp(route)
      const user = userEvent.setup()

      await user.click(await rollbackButton())

      await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent(`/services/${SERVICE_PAYMENTS.id}/deploy`))
      expect(screen.getByTestId('search')).toHaveTextContent('revision=5&review=1')
      expect(posts).toEqual([])
    } finally {
      server.events.removeListener('request:start', record)
    }
  })

  it('is hidden when no revision is offered', async () => {
    serveIncident({ rollback_revision: null })
    renderApp(route)

    await screen.findByText('Likely trigger')
    expect(screen.queryByRole('button', { name: /Roll back/ })).not.toBeInTheDocument()
  })

  it('is locked without Deploy and says why', async () => {
    server.use(http.get('/api/services/:id', () => HttpResponse.json({ ...SERVICE_PAYMENTS, can: { ...SERVICE_PAYMENTS.can, deploy: false } })))
    renderApp(route)

    const button = await rollbackButton()
    expect(button).toBeDisabled()
    expect(button).toHaveAttribute('title', `You need Deploy on ${SERVICE_PAYMENTS.tag.name}`)
  })
})
