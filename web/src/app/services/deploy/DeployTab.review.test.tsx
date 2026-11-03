import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import type { ChangeSetRequest, ChangeSetResponse } from '../../../api/deploy'
import { CHANGESET_APPLIED, CHANGESET_PARTIAL, DEPLOY_SETTINGS_PAYMENTS, SERVICE_PAYMENTS, TASK_DEFINITION_EDITABLE } from '../../../test/fixtures'
import { withCan } from '../../../test/deployVariants'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'

const route = `/services/${SERVICE_PAYMENTS.id}/deploy`

/** Captures every change set body; answers with `reply(callNumber)`. */
function captureChangeSets(reply: (call: number) => Response) {
  const bodies: ChangeSetRequest[] = []
  server.use(
    http.post('/api/services/:id/changeset', async ({ request }) => {
      bodies.push((await request.json()) as ChangeSetRequest)
      return reply(bodies.length)
    }),
  )
  return bodies
}

const footer = () => screen.getByRole('region', { name: 'Pending changes' })
const reviewButton = () => within(footer()).getByRole('button', { name: 'Review…' })
const dialog = () => screen.getByRole('dialog')
const desiredCount = () => within(screen.getByRole('region', { name: 'Service' })).getByRole('spinbutton', { name: 'Desired count' })

async function openDeployTab(path = route) {
  const view = renderApp(path)
  await within(await screen.findByRole('region', { name: 'Service' })).findByRole('spinbutton', { name: 'Desired count' })
  await within(await screen.findByRole('region', { name: 'Containers' })).findByRole('button', { name: 'Add variable' })
  return view
}

async function setDesiredCount(user: ReturnType<typeof userEvent.setup>, value: string) {
  const field = desiredCount()
  await user.clear(field)
  if (value) await user.type(field, value)
}

async function setEnvValue(user: ReturnType<typeof userEvent.setup>, value: string) {
  const field = within(screen.getByRole('region', { name: 'Containers' })).getByLabelText('Variable value 1')
  await user.clear(field)
  await user.type(field, value)
}

describe('DeployTab: footer', () => {
  it('no changes → Review disabled', async () => {
    await openDeployTab()
    expect(within(footer()).getByText('0 changes')).toBeInTheDocument()
    expect(reviewButton()).toBeDisabled()
    expect(within(footer()).getByRole('button', { name: 'Discard' })).toBeDisabled()
  })

  it('counts changed sections and Discard puts every draft back', async () => {
    const user = userEvent.setup()
    await openDeployTab()
    await setDesiredCount(user, '5')
    await setEnvValue(user, 'debug')
    expect(within(footer()).getByText('2 changes')).toBeInTheDocument()
    expect(reviewButton()).toBeEnabled()
    await user.click(within(footer()).getByRole('button', { name: 'Discard' }))
    expect(within(footer()).getByText('0 changes')).toBeInTheDocument()
    expect(desiredCount()).toHaveValue(3)
  })

  it('problems block Review', async () => {
    const user = userEvent.setup()
    await openDeployTab()
    await setDesiredCount(user, '5')
    expect(reviewButton()).toBeEnabled()
    await setDesiredCount(user, '')
    expect(reviewButton()).toBeDisabled()
    expect(within(footer()).getByText(/Desired count must be a whole number between 0 and 50/)).toBeInTheDocument()
  })

  it('?revision=N preloads the revision and shows deployed→N diff', async () => {
    const user = userEvent.setup()
    const bodies = captureChangeSets(() => HttpResponse.json(CHANGESET_APPLIED))
    await openDeployTab(`${route}?revision=41`)
    await waitFor(() => expect(within(footer()).getByText('1 change')).toBeInTheDocument())
    expect(within(footer()).getByText(':41 is loaded from the revisions list')).toBeInTheDocument()

    await user.click(reviewButton())
    const review = within(dialog())
    expect(review.getByText(/- .*"image": "payments-api:1\.18\.2"/)).toBeInTheDocument()
    expect(review.getByText(/\+ .*"image": "payments-api:1\.18\.1"/)).toBeInTheDocument()
    await user.click(review.getByRole('button', { name: 'Deploy :41' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0].task_definition).toEqual({ revision: 41 })
  })

  it('dismissing the preloaded revision clears it', async () => {
    const user = userEvent.setup()
    await openDeployTab(`${route}?revision=41`)
    await waitFor(() => expect(within(footer()).getByText('1 change')).toBeInTheDocument())
    await user.click(within(footer()).getByRole('button', { name: 'Dismiss :41' }))
    expect(within(footer()).getByText('0 changes')).toBeInTheDocument()
    expect(screen.getByTestId('search')).toHaveTextContent('')
  })
})

describe('Review', () => {
  it('lists the JSON diff, service rows and scaling rows with the reason', async () => {
    const user = userEvent.setup()
    await openDeployTab()
    await setEnvValue(user, 'debug')
    await setDesiredCount(user, '5')
    await user.clear(within(screen.getByRole('region', { name: 'Auto scaling' })).getByLabelText('Minimum'))
    await user.type(within(screen.getByRole('region', { name: 'Auto scaling' })).getByLabelText('Minimum'), '1')
    await user.click(reviewButton())

    const review = within(dialog())
    expect(review.getByText(/\+ .*"value": "debug"/)).toBeInTheDocument()
    expect(review.getByText(/- .*"value": "info"/)).toBeInTheDocument()
    const rows = review.getByRole('region', { name: 'Service changes' })
    expect(within(rows).getByText('Desired count')).toBeInTheDocument()
    expect(within(rows).getByText('3')).toBeInTheDocument()
    expect(within(rows).getByText('5')).toBeInTheDocument()
    expect(within(review.getByRole('region', { name: 'Auto scaling changes' })).getByText('Capacity 2–8 → 1–8')).toBeInTheDocument()
    expect(review.getByText('This is a Prod service.')).toBeInTheDocument()
    expect(review.getByText(/Registers revision :44 and deploys it to .*changes the service settings and changes auto scaling\./)).toBeInTheDocument()

    await user.type(review.getByRole('textbox', { name: 'Reason' }), 'raise limits')
    expect(review.getByText('12 / 500')).toBeInTheDocument()
    expect(review.getByRole('button', { name: 'Register and deploy :44' })).toBeEnabled()
  })

  it('sends reason and only changed sections', async () => {
    const user = userEvent.setup()
    const bodies = captureChangeSets(() => HttpResponse.json(CHANGESET_APPLIED))
    await openDeployTab()
    await setDesiredCount(user, '5')
    await user.click(reviewButton())
    await user.type(within(dialog()).getByRole('textbox', { name: 'Reason' }), 'scale up')
    await user.click(within(dialog()).getByRole('button', { name: 'Apply changes' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toEqual({ base_revision: 42, base_version: DEPLOY_SETTINGS_PAYMENTS.version, reason: 'scale up', service: { desired_count: 5 } })
  })

  it('partial result shows steps and Retry sends only failed sections with revision and settings_version', async () => {
    const user = userEvent.setup()
    const bodies = captureChangeSets((call) => HttpResponse.json(call === 1 ? CHANGESET_PARTIAL : CHANGESET_APPLIED))
    await openDeployTab()
    await setEnvValue(user, 'debug')
    await setDesiredCount(user, '5')
    await user.click(reviewButton())
    await user.type(within(dialog()).getByRole('textbox', { name: 'Reason' }), 'try')
    await user.click(within(dialog()).getByRole('button', { name: 'Register and deploy :44' }))

    const review = within(await screen.findByRole('dialog'))
    expect(await review.findByText('ECS rejected the update: InvalidParameterException.')).toBeInTheDocument()
    expect(review.getByText('Registered payments-api:43.')).toBeInTheDocument()
    expect(review.getByText('Skipped because the service step failed.')).toBeInTheDocument()

    await user.click(review.getByRole('button', { name: 'Retry remaining steps' }))
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1]).toEqual({
      base_revision: 42,
      base_version: CHANGESET_PARTIAL.settings_version,
      reason: 'try',
      task_definition: { revision: 43 },
      service: { desired_count: 5 },
    })
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('409 version conflict reopens with fresh settings banner', async () => {
    const user = userEvent.setup()
    const fresh = { ...DEPLOY_SETTINGS_PAYMENTS, version: 'v-fresh', desired_count: 4 }
    const bodies = captureChangeSets((call) =>
      call === 1
        ? HttpResponse.json({ error: { code: 'conflict', message: 'The settings changed.' }, settings: fresh }, { status: 409 })
        : HttpResponse.json(CHANGESET_APPLIED),
    )
    await openDeployTab()
    await setDesiredCount(user, '5')
    await user.click(reviewButton())
    await user.click(within(dialog()).getByRole('button', { name: 'Apply changes' }))

    const review = within(await screen.findByText('Settings changed while you were editing').then(() => dialog()))
    const rows = review.getByRole('region', { name: 'Service changes' })
    expect(within(rows).getByText('4')).toBeInTheDocument()
    expect(within(rows).getByText('5')).toBeInTheDocument()

    await user.click(review.getByRole('button', { name: 'Apply changes' }))
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1].base_version).toBe('v-fresh')
  })

  it('409 base conflict reopens with moved-from banner', async () => {
    const user = userEvent.setup()
    const bodies = captureChangeSets((call) =>
      call === 1
        ? HttpResponse.json({ error: { code: 'conflict', message: 'The service moved.' }, deployed_revision: 43 }, { status: 409 })
        : HttpResponse.json(CHANGESET_APPLIED),
    )
    server.use(
      http.get('/api/services/:id/task-definition', () =>
        HttpResponse.json(bodies.length > 0 ? { ...TASK_DEFINITION_EDITABLE, deployed_revision: 43 } : TASK_DEFINITION_EDITABLE),
      ),
    )
    await openDeployTab()
    await setEnvValue(user, 'debug')
    await user.click(reviewButton())
    await user.click(within(dialog()).getByRole('button', { name: 'Register and deploy :44' }))

    expect(await screen.findByText('The service moved from :42 to :43 while you were editing. Check the diff before applying.')).toBeInTheDocument()
    await user.click(within(dialog()).getByRole('button', { name: 'Register and deploy :44' }))
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1].base_revision).toBe(43)
  })

  it('422 shows the server message inside the dialog', async () => {
    const user = userEvent.setup()
    captureChangeSets(() => HttpResponse.json({ error: { code: 'invalid', message: 'Nothing to change' } }, { status: 422 }))
    await openDeployTab()
    await setDesiredCount(user, '5')
    await user.click(reviewButton())
    await user.click(within(dialog()).getByRole('button', { name: 'Apply changes' }))
    expect(await within(dialog()).findByText('Nothing to change')).toBeInTheDocument()
  })

  it('Edit-only user registers only', async () => {
    const user = userEvent.setup()
    server.use(http.get('/api/services/:id/deploy-settings', () => HttpResponse.json(withCan(DEPLOY_SETTINGS_PAYMENTS, { deploy: false, operate: false }))))
    const bodies = captureChangeSets(() =>
      HttpResponse.json({ ...CHANGESET_APPLIED, status: 'registered_only', registered_revision: 44, deployment_id: '', from_revision: null, to_revision: null } satisfies ChangeSetResponse),
    )
    await openDeployTab()
    await setEnvValue(user, 'debug')
    await user.click(reviewButton())
    await user.click(within(dialog()).getByRole('button', { name: 'Register :44' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(Object.keys(bodies[0]).sort()).toEqual(['base_revision', 'base_version', 'reason', 'task_definition'])
    expect(await screen.findByText('Registered :44. Someone with Deploy on Prod can roll it out.')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('applied resets drafts and toasts', async () => {
    const user = userEvent.setup()
    captureChangeSets(() => HttpResponse.json(CHANGESET_APPLIED))
    await openDeployTab()
    await setDesiredCount(user, '5')
    await setEnvValue(user, 'debug')
    await user.click(reviewButton())
    await user.click(within(dialog()).getByRole('button', { name: 'Register and deploy :44' }))

    expect(await screen.findByText('Deploying payments-api to :43.')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(within(footer()).getByText('0 changes')).toBeInTheDocument()
    expect(desiredCount()).toHaveValue(3)
    expect(within(screen.getByRole('region', { name: 'Containers' })).getByLabelText('Variable value 1')).toHaveValue('info')
  })
})
