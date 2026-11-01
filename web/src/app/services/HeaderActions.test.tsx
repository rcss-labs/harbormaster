import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import type { DesiredCountResponse, ServiceDTO } from '../../api/types'
import { SERVICE_PAYMENTS } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderWithProviders } from '../../test/setup'
import { HeaderActions } from './HeaderActions'

const rolling: ServiceDTO = {
  ...SERVICE_PAYMENTS,
  deployments: [{ ...SERVICE_PAYMENTS.deployments[0], rollout_state: 'IN_PROGRESS' }],
}

const withoutOperate: ServiceDTO = { ...SERVICE_PAYMENTS, can: { ...SERVICE_PAYMENTS.can, operate: false } }

const unsupported: ServiceDTO = {
  ...SERVICE_PAYMENTS,
  controller: { ...SERVICE_PAYMENTS.controller, supported: false, banner: 'Managed by the ECS controller.' },
}

function desiredCountInfo(overrides: Partial<DesiredCountResponse> = {}) {
  return http.get(`/api/services/${SERVICE_PAYMENTS.id}/desired-count`, () =>
    HttpResponse.json({ desired: SERVICE_PAYMENTS.desired, running: SERVICE_PAYMENTS.running, max: 50, auto_scaling: null, ...overrides }),
  )
}

function renderActions(service: ServiceDTO = SERVICE_PAYMENTS) {
  return renderWithProviders(<HeaderActions serviceId={service.id} service={service} />)
}

describe('HeaderActions restart', () => {
  it('disables Restart while a rollout is running', () => {
    renderActions(rolling)
    expect(screen.getByRole('button', { name: 'Restart (new deployment)' })).toBeDisabled()
  })

  it('disables Restart without Operate and names the tag in the title', () => {
    renderActions(withoutOperate)
    const restart = screen.getByRole('button', { name: 'Restart (new deployment)' })
    expect(restart).toBeDisabled()
    expect(restart).toHaveAttribute('title', `You need Operate on ${SERVICE_PAYMENTS.tag.name}`)
  })

  it('disables Restart when the controller is unsupported and shows the banner as its title', () => {
    renderActions(unsupported)
    const restart = screen.getByRole('button', { name: 'Restart (new deployment)' })
    expect(restart).toBeDisabled()
    expect(restart).toHaveAttribute('title', 'Managed by the ECS controller.')
  })

  it('confirms the restart, posts once and closes the dialog', async () => {
    const restartRequests = vi.fn()
    server.use(
      http.post(`/api/services/${SERVICE_PAYMENTS.id}/restart`, () => {
        restartRequests()
        return HttpResponse.json(
          { deployment_id: 'dep-1', revision: SERVICE_PAYMENTS.revision, kind: 'restart' },
          { status: 202 },
        )
      }),
    )
    renderActions()
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: 'Restart (new deployment)' }))
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveTextContent(`Restart ${SERVICE_PAYMENTS.name}?`)
    expect(dialog).toHaveTextContent(
      `Starts a new deployment of :${SERVICE_PAYMENTS.revision}. Each task is replaced, new ones first.`,
    )
    expect(restartRequests).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: 'Restart' }))
    await waitFor(() => expect(restartRequests).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('shows the recording warning from a restart response as a toast', async () => {
    server.use(
      http.post(`/api/services/${SERVICE_PAYMENTS.id}/restart`, () =>
        HttpResponse.json({ deployment_id: 'dep-1', revision: SERVICE_PAYMENTS.revision, kind: 'restart', warning: 'Rolled out, but Harbormaster could not record it in the operations history.' }, { status: 202 }),
      ),
    )
    renderActions()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Restart (new deployment)' }))
    await user.click(screen.getByRole('button', { name: 'Restart' }))
    expect(await screen.findByText('Rolled out, but Harbormaster could not record it in the operations history.')).toBeInTheDocument()
  })

  it('disables the confirm button while the restart request is pending', async () => {
    const restartRequests = vi.fn()
    let releaseRequest: () => void = () => {}
    const gate = new Promise<void>((resolve) => {
      releaseRequest = resolve
    })
    server.use(
      http.post(`/api/services/${SERVICE_PAYMENTS.id}/restart`, async () => {
        restartRequests()
        await gate
        return HttpResponse.json({ deployment_id: 'dep-1', revision: SERVICE_PAYMENTS.revision, kind: 'restart' }, { status: 202 })
      }),
    )
    renderActions()
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: 'Restart (new deployment)' }))
    await user.click(screen.getByRole('button', { name: 'Restart' }))
    await waitFor(() => expect(restartRequests).toHaveBeenCalledTimes(1))

    const pendingConfirm = screen.getByRole('button', { name: 'Restart' })
    expect(pendingConfirm).toBeDisabled()
    await user.click(pendingConfirm)
    expect(restartRequests).toHaveBeenCalledTimes(1)

    releaseRequest()
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('shows a rollout-in-progress toast when the server answers 409', async () => {
    server.use(
      http.post(`/api/services/${SERVICE_PAYMENTS.id}/restart`, () =>
        HttpResponse.json({ error: { code: 'conflict', message: 'rollout in progress' } }, { status: 409 }),
      ),
    )
    renderActions()
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: 'Restart (new deployment)' }))
    await user.click(screen.getByRole('button', { name: 'Restart' }))

    expect(await screen.findByText('A rollout is already running')).toBeInTheDocument()
  })
})

describe('HeaderActions desired count', () => {
  it('shows the warning from a desired count response as a toast', async () => {
    server.use(
      desiredCountInfo(),
      http.post(`/api/services/${SERVICE_PAYMENTS.id}/desired-count`, () =>
        HttpResponse.json({ desired: 0, previous: 3, changed: true, warning: 'Auto scaling may change it back.' }),
      ),
    )
    renderActions()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: `Desired count: ${SERVICE_PAYMENTS.desired}` }))
    const input = await screen.findByRole('spinbutton')
    await user.clear(input)
    await user.type(input, '0')
    await user.click(screen.getByRole('button', { name: 'Update desired count' }))
    expect(await screen.findByText('Auto scaling may change it back.')).toBeInTheDocument()
  })

  it('shows the zero sentence and posts nothing until confirmed', async () => {
    const countRequests = vi.fn()
    server.use(
      desiredCountInfo(),
      http.post(`/api/services/${SERVICE_PAYMENTS.id}/desired-count`, async ({ request }) => {
        countRequests(await request.json())
        return HttpResponse.json({ desired: 0, previous: 3, changed: true, warning: null })
      }),
    )
    renderActions()
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: `Desired count: ${SERVICE_PAYMENTS.desired}` }))
    const input = await screen.findByRole('spinbutton')
    await user.clear(input)
    await user.type(input, '0')

    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveTextContent(`Set ${SERVICE_PAYMENTS.name} to 0 tasks?`)
    expect(dialog).toHaveTextContent(`Now ${SERVICE_PAYMENTS.desired}. Every task stops. The service and its definition stay.`)
    expect(countRequests).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: 'Update desired count' }))
    await waitFor(() => expect(countRequests).toHaveBeenCalledWith({ desired: 0 }))
  })

  it('warns when auto scaling would move the value back into its range', async () => {
    server.use(desiredCountInfo({ auto_scaling: { min: 2, max: 6 } }))
    renderActions()
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: `Desired count: ${SERVICE_PAYMENTS.desired}` }))
    const input = await screen.findByRole('spinbutton')
    await user.clear(input)
    await user.type(input, '8')

    expect(
      await screen.findByText('Auto scaling keeps this service between 2 and 6. It will move 8 back into that range.'),
    ).toBeInTheDocument()
  })

  it('links to the Scaling tab and closes the dialog when followed', async () => {
    server.use(desiredCountInfo({ auto_scaling: { min: 2, max: 6 } }))
    renderActions()
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: `Desired count: ${SERVICE_PAYMENTS.desired}` }))
    const link = await screen.findByRole('link', { name: 'Auto scaling settings →' })
    expect(link).toHaveAttribute('href', `/services/${SERVICE_PAYMENTS.id}/scaling`)

    await user.click(link)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('shows a toast and posts nothing when the value is unchanged', async () => {
    const countRequests = vi.fn()
    server.use(
      desiredCountInfo(),
      http.post(`/api/services/${SERVICE_PAYMENTS.id}/desired-count`, () => {
        countRequests()
        return HttpResponse.json({ desired: 0, previous: 0, changed: false, warning: null })
      }),
    )
    renderActions()
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: `Desired count: ${SERVICE_PAYMENTS.desired}` }))
    await screen.findByRole('spinbutton')
    await user.click(screen.getByRole('button', { name: 'Update desired count' }))

    expect(await screen.findByText(`Desired count is already ${SERVICE_PAYMENTS.desired}.`)).toBeInTheDocument()
    expect(countRequests).not.toHaveBeenCalled()
  })
})
