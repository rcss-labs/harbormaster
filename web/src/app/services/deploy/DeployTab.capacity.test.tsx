import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import type { ChangeSetRequest, DeploySettingsResponse } from '../../../api/deploy'
import { CHANGESET_APPLIED, DEPLOY_SETTINGS_PAYMENTS, SERVICE_PAYMENTS } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'

const route = `/services/${SERVICE_PAYMENTS.id}/deploy`
const PRESET = 'capacity=spot&on_demand_base=1'
const CLUSTER_COPY =
  'Cluster core-services has no FARGATE_SPOT capacity provider. Associate it with the cluster (in Terraform: aws_ecs_cluster_capacity_providers), then this service qualifies.'
const WARNING =
  "Spot tasks can be stopped with two minutes' notice, and ECS does not fall back to on-demand when Spot capacity runs out. Applying this starts a new deployment that replaces every task."

const serveSettings = (settings: DeploySettingsResponse) => server.use(http.get('/api/services/:id/deploy-settings', () => HttpResponse.json(settings)))
const withCapacity = (capacity: Partial<DeploySettingsResponse['capacity']>): DeploySettingsResponse => ({
  ...DEPLOY_SETTINGS_PAYMENTS,
  capacity: { ...DEPLOY_SETTINGS_PAYMENTS.capacity, ...capacity },
})

const captureChangeSets = () => {
  const bodies: ChangeSetRequest[] = []
  server.use(
    http.post('/api/services/:id/changeset', async ({ request }) => {
      bodies.push((await request.json()) as ChangeSetRequest)
      return HttpResponse.json(CHANGESET_APPLIED)
    }),
  )
  return bodies
}

const pendingRegion = () => within(screen.getByRole('region', { name: 'Pending changes' }))
const base = () => screen.findByRole('spinbutton', { name: 'On-demand tasks kept (base)' })
const onDemandWeight = () => screen.getByRole('spinbutton', { name: 'On-demand weight' })
const spotWeight = () => screen.getByRole('spinbutton', { name: 'Spot weight' })

describe('DeployTab: a Fargate Spot suggestion from the Cost tab', () => {
  it('fills Spot from the link and uses the Spot reason', async () => {
    const user = userEvent.setup()
    const bodies = captureChangeSets()
    renderApp(`${route}?${PRESET}`)

    expect(await base()).toHaveValue(1)
    expect(onDemandWeight()).toHaveValue(0)
    expect(spotWeight()).toHaveValue(1)
    const pending = pendingRegion()
    expect(await pending.findByRole('listitem', { name: 'Capacity providers' })).toHaveTextContent('FARGATE (launch type) → FARGATE base 1, FARGATE_SPOT weight 1')
    expect(pending.getByText('Fargate Spot with 1 on-demand is suggested from Cost')).toBeInTheDocument()
    expect(bodies).toHaveLength(0)

    await user.click(pending.getByRole('button', { name: 'Review…' }))
    const dialog = within(screen.getByRole('dialog'))
    expect(dialog.getByRole('textbox', { name: 'Reason' })).toHaveValue('Move to Fargate Spot, 1 on-demand')
    expect(bodies).toHaveLength(0)
    await user.click(dialog.getByRole('button', { name: /^(Apply|Deploy|Register)/ }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0].reason).toBe('Move to Fargate Spot, 1 on-demand')
    expect(bodies[0].service?.capacity_provider_strategy).toEqual([
      { provider: 'FARGATE', base: 1, weight: 0 },
      { provider: 'FARGATE_SPOT', base: 0, weight: 1 },
    ])
    expect(bodies[0].task_definition).toBeUndefined()
  })

  it('warns that every task is replaced', async () => {
    const user = userEvent.setup()
    renderApp(route)
    const field = spotWeight
    await base()
    expect(screen.queryByText(WARNING)).not.toBeInTheDocument()

    await user.clear(field())
    await user.type(field(), '1')
    expect(await screen.findByText(WARNING)).toBeInTheDocument()
    await user.clear(field())
    await user.type(field(), '0')
    await waitFor(() => expect(screen.queryByText(WARNING)).not.toBeInTheDocument())
  })

  it('locks Spot when the cluster has no FARGATE_SPOT', async () => {
    serveSettings(withCapacity({ cluster_providers: ['FARGATE'] }))
    renderApp(`${route}?${PRESET}`)

    await base()
    const spot = spotWeight()
    expect(spot).toBeDisabled()
    expect(spot.closest('label')).toHaveAttribute('title', CLUSTER_COPY)
    // The link is not applied: nothing is filled in and the server would refuse it anyway.
    expect(spot).toHaveValue(0)
    expect(pendingRegion().queryAllByRole('listitem')).toHaveLength(0)
    expect(pendingRegion().queryByText(/suggested from Cost/)).not.toBeInTheDocument()
  })

  it('ignores the link without Deploy', async () => {
    serveSettings({ ...DEPLOY_SETTINGS_PAYMENTS, can: { ...DEPLOY_SETTINGS_PAYMENTS.can, deploy: false } })
    renderApp(`${route}?${PRESET}`)

    await base()
    expect(spotWeight()).toBeDisabled()
    expect(spotWeight()).toHaveValue(0)
    expect(pendingRegion().queryAllByRole('listitem')).toHaveLength(0)
    expect(pendingRegion().queryByText(/suggested from Cost/)).not.toBeInTheDocument()
  })

  it('locks the fields with the reason when the service cannot be edited', async () => {
    serveSettings(withCapacity({ editable: false, lock_reason: 'This service does not run on Fargate.', launch_type: 'EC2' }))
    renderApp(`${route}?${PRESET}`)

    expect(await base()).toBeDisabled()
    expect(onDemandWeight()).toBeDisabled()
    expect(spotWeight().closest('label')).toHaveAttribute('title', 'This service does not run on Fargate.')
    expect(pendingRegion().queryAllByRole('listitem')).toHaveLength(0)
  })

  it('says the service already uses this strategy', async () => {
    serveSettings(
      withCapacity({
        launch_type: '',
        strategy: [
          { provider: 'FARGATE', base: 1, weight: 0 },
          { provider: 'FARGATE_SPOT', base: 0, weight: 1 },
        ],
      }),
    )
    renderApp(`${route}?${PRESET}`)

    await base()
    expect(await pendingRegion().findByText('This service already uses this capacity strategy.')).toBeInTheDocument()
    expect(pendingRegion().queryAllByRole('listitem')).toHaveLength(0)
  })

  it.each([
    [
      'apply',
      async (user: ReturnType<typeof userEvent.setup>) => {
        await user.click(pendingRegion().getByRole('button', { name: 'Review…' }))
        await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: /^(Apply|Deploy|Register)/ }))
      },
    ],
    [
      'discard',
      async (user: ReturnType<typeof userEvent.setup>) => {
        await user.click(pendingRegion().getByRole('button', { name: 'Discard' }))
      },
    ],
  ])('clears the capacity parameters after %s', async (_name, finish) => {
    const user = userEvent.setup()
    captureChangeSets()
    renderApp(`${route}?${PRESET}`)
    await base()
    await pendingRegion().findByRole('listitem', { name: 'Capacity providers' })

    await finish(user)

    await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent(/^$/))
    await waitFor(() => expect(pendingRegion().queryAllByRole('listitem')).toHaveLength(0))
    expect(spotWeight()).toHaveValue(0)
  })

  it('combines with a rightsizing link in one reason', async () => {
    const user = userEvent.setup()
    renderApp(`${route}?${PRESET}&task_cpu=256&task_memory=512&headroom=30`)
    await base()
    await pendingRegion().findByRole('listitem', { name: 'Capacity providers' })
    await pendingRegion().findByRole('listitem', { name: 'Task CPU' })

    await user.click(pendingRegion().getByRole('button', { name: 'Review…' }))
    expect(within(screen.getByRole('dialog')).getByRole('textbox', { name: 'Reason' })).toHaveValue('Rightsizing at 30% headroom; Move to Fargate Spot, 1 on-demand')
  })

  it('drops the prefilled reason once the user changes the strategy', async () => {
    const user = userEvent.setup()
    renderApp(`${route}?${PRESET}`)
    await base()
    await pendingRegion().findByRole('listitem', { name: 'Capacity providers' })
    await user.clear(spotWeight())
    await user.type(spotWeight(), '2')

    await user.click(pendingRegion().getByRole('button', { name: 'Review…' }))
    expect(within(screen.getByRole('dialog')).getByRole('textbox', { name: 'Reason' })).toHaveValue('')
  })
})
