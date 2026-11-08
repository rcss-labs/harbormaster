import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import type { ChangeSetRequest } from '../../../api/deploy'
import { CHANGESET_APPLIED, DEPLOY_SETTINGS_PARTIAL, DEPLOY_SETTINGS_PAYMENTS, SERVICE_PAYMENTS } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'

const route = `/services/${SERVICE_PAYMENTS.id}/deploy`
const TOOLTIP = 'Scheduled actions could not be read'

const servePartialSettings = () => server.use(http.get('/api/services/:id/deploy-settings', () => HttpResponse.json(DEPLOY_SETTINGS_PARTIAL)))

describe('DeployTab: partial deploy settings', () => {
  it('locks only the scheduled actions editor', async () => {
    servePartialSettings()
    renderApp(route)
    await screen.findByText(/Scheduled actions could not be read: UnsupportedOperation/)
    const scaling = within(screen.getByRole('region', { name: 'Auto scaling' }))
    const add = scaling.getByRole('button', { name: 'Add scheduled action' })
    expect(add).toBeDisabled()
    expect(add).toHaveAttribute('title', TOOLTIP)
    // Everything else stays usable.
    expect(scaling.getByLabelText('Minimum')).toBeEnabled()
    expect(scaling.getByLabelText('Maximum')).toBeEnabled()
    expect(scaling.getByRole('button', { name: 'Add policy' })).toBeEnabled()
    expect(scaling.getByRole('button', { name: 'Edit cpu-target-60' })).toBeEnabled()
    expect(within(screen.getByRole('region', { name: 'Service' })).getByRole('spinbutton', { name: 'Desired count' })).toBeEnabled()
    expect(await screen.findByRole('button', { name: 'Add variable' })).toBeEnabled()
  })

  it('Retry refetches deploy settings', async () => {
    const user = userEvent.setup()
    servePartialSettings()
    renderApp(route)
    await screen.findByText(/Scheduled actions could not be read/)
    const scaling = within(screen.getByRole('region', { name: 'Auto scaling' }))

    server.use(http.get('/api/services/:id/deploy-settings', () => HttpResponse.json(DEPLOY_SETTINGS_PAYMENTS)))
    await user.click(scaling.getByRole('button', { name: 'Retry' }))

    await waitFor(() => expect(scaling.queryByText(/could not be read/)).not.toBeInTheDocument())
    expect(scaling.getByText('business-hours')).toBeInTheDocument()
    expect(scaling.getByRole('button', { name: 'Add scheduled action' })).toBeEnabled()
  })

  it('Review is enabled with partial when the draft does not touch scheduled actions', async () => {
    const user = userEvent.setup()
    servePartialSettings()
    const bodies: ChangeSetRequest[] = []
    server.use(
      http.post('/api/services/:id/changeset', async ({ request }) => {
        bodies.push((await request.json()) as ChangeSetRequest)
        return HttpResponse.json(CHANGESET_APPLIED)
      }),
    )
    renderApp(route)
    const field = await waitFor(() => within(screen.getByRole('region', { name: 'Service' })).getByRole('spinbutton', { name: 'Desired count' }))
    await screen.findByRole('button', { name: 'Add variable' })
    await user.clear(field)
    await user.type(field, '5')

    const pending = within(screen.getByRole('region', { name: 'Pending changes' }))
    expect(pending.getByRole('listitem', { name: 'Desired count' })).toHaveTextContent('Desired count: 3 → 5')
    await user.click(pending.getByRole('button', { name: 'Review…' }))
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Apply changes' }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0].scaling).toBeUndefined()
    expect(bodies[0].service?.desired_count).toBe(5)
  })

  it('degrades only the failed part: failing settings keep Containers and Task size', async () => {
    server.use(http.get('/api/services/:id/deploy-settings', () => HttpResponse.json({ error: 'upstream' }, { status: 502 })))
    renderApp(route)

    await screen.findAllByRole('button', { name: 'Retry' })
    expect(within(screen.getByRole('region', { name: 'Service' })).getByRole('button', { name: 'Retry' })).toBeInTheDocument()
    expect(within(screen.getByRole('region', { name: 'Auto scaling' })).getByRole('button', { name: 'Retry' })).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Add variable' })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Task size' })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Pending changes' })).toBeInTheDocument()
  })

  it('shows skeletons, not a blank, while the settings load', async () => {
    server.use(http.get('/api/services/:id/deploy-settings', () => new Promise(() => {})))
    renderApp(route)
    const service = await screen.findByRole('region', { name: 'Service' })
    expect(within(service).getByRole('status', { name: 'Loading' })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Auto scaling' })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Pending changes' })).toBeInTheDocument()
  })
})
