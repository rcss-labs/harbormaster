import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { SCALING_NONE, SCALING_PAYMENTS, SERVICE_PAYMENTS } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'

const desiredInfo = (
  overrides: Partial<{
    desired: number
    running: number
    max: number
    auto_scaling: { min: number; max: number } | null
  }> = {},
) => ({
  desired: 3,
  running: 3,
  max: 50,
  auto_scaling: { min: SCALING_PAYMENTS.min, max: SCALING_PAYMENTS.max },
  ...overrides,
})

function mockDesiredCount(info = desiredInfo(), scaling = SCALING_PAYMENTS) {
  server.use(
    http.get('/api/services/:id/desired-count', () => HttpResponse.json(info)),
    http.get('/api/services/:id/scaling', () => HttpResponse.json(scaling)),
  )
}

/** The value of the definition whose term is `label`, matched on its full text so split spans still match. */
const definitionFor = (label: string) =>
  screen.getByText((_, element) => element?.tagName === 'DT' && element.textContent === label).nextElementSibling as HTMLElement

describe('ScalingTab', () => {
  it('links to the Deploy tab when the user can operate', async () => {
    mockDesiredCount()
    renderApp(`/services/${SERVICE_PAYMENTS.id}/scaling`)
    // First render in this file pays for loading the whole app; the default 1 s wait flakes under load.
    const link = await screen.findByRole('link', { name: 'Edit in Deploy →' }, { timeout: 5000 })
    expect(link).toHaveAttribute('href', `/services/${SERVICE_PAYMENTS.id}/deploy`)
  })

  it('hides the Edit in Deploy link without Operate', async () => {
    mockDesiredCount()
    server.use(
      http.get('/api/services/:id', () => HttpResponse.json({ ...SERVICE_PAYMENTS, can: { ...SERVICE_PAYMENTS.can, operate: false } })),
    )
    renderApp(`/services/${SERVICE_PAYMENTS.id}/scaling`)
    await screen.findByText('Auto scaling')
    expect(screen.queryByRole('link', { name: 'Edit in Deploy →' })).not.toBeInTheDocument()
  })

  it('renders the readable parts and flags the unreadable scheduled actions, never "no scalable target"', async () => {
    mockDesiredCount(desiredInfo(), { ...SCALING_PAYMENTS, scheduled_actions: [], unreadable_parts: ['scheduled_actions'] })
    renderApp(`/services/${SERVICE_PAYMENTS.id}/scaling`)

    expect(await screen.findByText('Auto scaling')).toBeInTheDocument()
    expect(definitionFor('Minimum tasks')).toHaveTextContent(String(SCALING_PAYMENTS.min))
    expect(definitionFor('Maximum tasks')).toHaveTextContent(String(SCALING_PAYMENTS.max))
    expect(definitionFor('Policy')).toHaveTextContent('ECSServiceAverageCPUUtilization')
    expect(definitionFor('Scheduled actions')).toHaveTextContent('Scheduled actions could not be read')
    expect(screen.queryByText('This service has no scalable target registered.')).not.toBeInTheDocument()
  })

  it('says the scalable target could not be read instead of claiming there is none', async () => {
    mockDesiredCount(desiredInfo({ auto_scaling: null }), { ...SCALING_NONE, unreadable_parts: ['scaling_target'] })
    renderApp(`/services/${SERVICE_PAYMENTS.id}/scaling`)

    expect(await screen.findByText('The scalable target could not be read.')).toBeInTheDocument()
    expect(screen.queryByText('This service has no scalable target registered.')).not.toBeInTheDocument()
  })

  it('renders min, max, policy and scheduled actions', async () => {
    mockDesiredCount()
    renderApp(`/services/${SERVICE_PAYMENTS.id}/scaling`)

    expect(await screen.findByText('Auto scaling')).toBeInTheDocument()
    expect(definitionFor('Minimum tasks')).toHaveTextContent(String(SCALING_PAYMENTS.min))
    expect(definitionFor('Maximum tasks')).toHaveTextContent(String(SCALING_PAYMENTS.max))
    expect(definitionFor('Policy')).toHaveTextContent('Target tracking · ECSServiceAverageCPUUtilization at 60%')
    expect(definitionFor('Scale-out cooldown')).toHaveTextContent('60 s')
    expect(definitionFor('Scale-in cooldown')).toHaveTextContent('300 s')
    expect(definitionFor('Scheduled actions')).toHaveTextContent('business-hours: cron(0 7 * * ? *) → min 3')
  })

  it('draws the auto scaling range with the desired count marked', async () => {
    mockDesiredCount()
    renderApp(`/services/${SERVICE_PAYMENTS.id}/scaling`)

    const bar = await screen.findByRole('img', { name: `Auto scaling range ${SCALING_PAYMENTS.min} to ${SCALING_PAYMENTS.max} tasks, desired 3` })
    expect(bar).toBeInTheDocument()
  })

  it('omits the range bar without a scalable target', async () => {
    mockDesiredCount(desiredInfo({ auto_scaling: null }), SCALING_NONE)
    renderApp(`/services/${SERVICE_PAYMENTS.id}/scaling`)

    await screen.findByText('This service has no scalable target registered.')
    expect(screen.queryByRole('img', { name: /Auto scaling range/ })).not.toBeInTheDocument()
  })

  it('stepper posts the new count and toasts', async () => {
    mockDesiredCount()
    const posted: unknown[] = []
    server.use(
      http.post('/api/services/:id/desired-count', async ({ request }) => {
        posted.push(await request.json())
        return HttpResponse.json({
          desired: 4,
          previous: 3,
          changed: true,
          warning: null,
        })
      }),
    )
    const user = userEvent.setup()
    renderApp(`/services/${SERVICE_PAYMENTS.id}/scaling`)

    const input = await screen.findByRole('spinbutton', {
      name: 'Scale to',
    })
    expect(input).toHaveValue(3)
    await user.click(screen.getByRole('button', { name: 'More tasks' }))
    expect(input).toHaveValue(4)
    await user.click(screen.getByRole('button', { name: 'Update desired count' }))

    const dialog = await screen.findByRole('dialog')
    expect(dialog).toHaveTextContent(`Set ${SERVICE_PAYMENTS.name} to 4 tasks?`)
    expect(posted).toEqual([])
    await user.click(within(dialog).getByRole('button', { name: 'Update desired count' }))

    expect(await screen.findByText('Desired count set to 4.')).toBeInTheDocument()
    expect(posted).toEqual([{ desired: 4 }])
  })

  it('cancelling the confirm dialog posts nothing', async () => {
    mockDesiredCount()
    const posted = vi.fn()
    server.use(
      http.post('/api/services/:id/desired-count', () => {
        posted()
        return HttpResponse.json({ desired: 4, previous: 3, changed: true, warning: null })
      }),
    )
    const user = userEvent.setup()
    renderApp(`/services/${SERVICE_PAYMENTS.id}/scaling`)

    await screen.findByRole('spinbutton', { name: 'Scale to' })
    await user.click(screen.getByRole('button', { name: 'More tasks' }))
    await user.click(screen.getByRole('button', { name: 'Update desired count' }))
    await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Cancel' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(posted).not.toHaveBeenCalled()
    expect(screen.getByRole('spinbutton', { name: 'Scale to' })).toHaveValue(4)
  })

  it('keeps the draft and the open confirm dialog when a poll changes the running count', async () => {
    mockDesiredCount()
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const user = userEvent.setup()
    renderApp(`/services/${SERVICE_PAYMENTS.id}/scaling`, queryClient)

    await screen.findByRole('spinbutton', { name: 'Scale to' })
    await user.click(screen.getByRole('button', { name: 'More tasks' }))
    await user.click(screen.getByRole('button', { name: 'Update desired count' }))
    await screen.findByRole('dialog')

    act(() => queryClient.setQueryData(['service', SERVICE_PAYMENTS.id, 'desired-count'], desiredInfo({ desired: 6 })))

    await screen.findByText(/Running 3 of 6\./)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByRole('spinbutton', { name: 'Scale to' })).toHaveValue(4)
  })

  it('follows the running count from a poll when the draft is untouched', async () => {
    mockDesiredCount()
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    renderApp(`/services/${SERVICE_PAYMENTS.id}/scaling`, queryClient)

    await screen.findByRole('spinbutton', { name: 'Scale to' })
    act(() => queryClient.setQueryData(['service', SERVICE_PAYMENTS.id, 'desired-count'], desiredInfo({ desired: 6 })))

    await waitFor(() => expect(screen.getByRole('spinbutton', { name: 'Scale to' })).toHaveValue(6))
  })

  it('warns when the value is outside the auto scaling range', async () => {
    mockDesiredCount()
    const user = userEvent.setup()
    renderApp(`/services/${SERVICE_PAYMENTS.id}/scaling`)

    const input = await screen.findByRole('spinbutton', {
      name: 'Scale to',
    })
    expect(screen.queryByText(/Outside the auto scaling range/)).not.toBeInTheDocument()
    await user.clear(input)
    await user.type(input, String(SCALING_PAYMENTS.max + 1))

    expect(screen.getByText(`Outside the auto scaling range ${SCALING_PAYMENTS.min}–${SCALING_PAYMENTS.max}.`)).toBeInTheDocument()
  })

  it('button disabled without Operate with the title', async () => {
    mockDesiredCount()
    server.use(
      http.get('/api/services/:id', () =>
        HttpResponse.json({
          ...SERVICE_PAYMENTS,
          can: { ...SERVICE_PAYMENTS.can, operate: false },
        }),
      ),
    )
    renderApp(`/services/${SERVICE_PAYMENTS.id}/scaling`)

    const button = await screen.findByRole('button', {
      name: 'Update desired count',
    })
    expect(button).toBeDisabled()
    expect(button).toHaveAttribute('title', `You need Operate on ${SERVICE_PAYMENTS.tag.name}`)
  })

  it('a failed scaling request errors only the scaling panels, keeps the stepper, and Retry refetches', async () => {
    let calls = 0
    server.use(
      http.get('/api/services/:id/desired-count', () => HttpResponse.json(desiredInfo())),
      http.get('/api/services/:id/scaling', () => {
        calls += 1
        return calls === 1
          ? HttpResponse.json({ error: { code: 'aws_error', message: 'Scaling is unavailable' } }, { status: 502 })
          : HttpResponse.json(SCALING_PAYMENTS)
      }),
    )
    renderApp(`/services/${SERVICE_PAYMENTS.id}/scaling`)

    const autoScaling = await screen.findByRole('region', { name: 'Auto scaling' })
    expect(await within(autoScaling).findByRole('alert')).toHaveTextContent('Auto scaling could not be read.')
    expect(screen.getByRole('spinbutton', { name: 'Scale to' })).toBeInTheDocument()
    await userEvent.click(within(autoScaling).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Minimum tasks')).toBeInTheDocument()
  })

  it('no target shows the empty state and the no-auto-scaling hint', async () => {
    mockDesiredCount(desiredInfo({ auto_scaling: null }), SCALING_NONE)
    renderApp(`/services/${SERVICE_PAYMENTS.id}/scaling`)

    expect(await screen.findByText('This service has no scalable target registered.')).toBeInTheDocument()
    expect(screen.getByText(/No auto scaling: the count stays where you set it\./)).toBeInTheDocument()
    expect(screen.queryByText('Minimum tasks')).not.toBeInTheDocument()
  })
})
