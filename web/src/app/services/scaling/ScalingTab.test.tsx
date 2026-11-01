import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
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
      name: 'Desired count',
    })
    expect(input).toHaveValue(3)
    await user.click(screen.getByRole('button', { name: 'More tasks' }))
    expect(input).toHaveValue(4)
    await user.click(screen.getByRole('button', { name: 'Update desired count' }))

    expect(await screen.findByText('Desired count set to 4.')).toBeInTheDocument()
    expect(posted).toEqual([{ desired: 4 }])
  })

  it('warns when the value is outside the auto scaling range', async () => {
    mockDesiredCount()
    const user = userEvent.setup()
    renderApp(`/services/${SERVICE_PAYMENTS.id}/scaling`)

    const input = await screen.findByRole('spinbutton', {
      name: 'Desired count',
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

  it('no target shows the empty state and the no-auto-scaling hint', async () => {
    mockDesiredCount(desiredInfo({ auto_scaling: null }), SCALING_NONE)
    renderApp(`/services/${SERVICE_PAYMENTS.id}/scaling`)

    expect(await screen.findByText('This service has no scalable target registered.')).toBeInTheDocument()
    expect(screen.getByText(/No auto scaling: the count stays where you set it\./)).toBeInTheDocument()
    expect(screen.queryByText('Minimum tasks')).not.toBeInTheDocument()
  })
})
