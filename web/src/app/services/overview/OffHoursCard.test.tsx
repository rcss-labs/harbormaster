import { screen, waitFor, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import type { ServiceOffHours } from '../../../api/offhours'
import { SERVICE_PAYMENTS } from '../../../test/fixtures'
import { SERVICE_OFFHOURS_ASLEEP, SERVICE_OFFHOURS_BLOCKED, SERVICE_OFFHOURS_NONE, SERVICE_OFFHOURS_WOKEN } from '../../../test/offhoursFixtures'
import { server } from '../../../test/msw'
import { renderWithProviders } from '../../../test/setup'
import { OffHoursCard } from './OffHoursCard'

const offHoursPath = `/api/services/${SERVICE_PAYMENTS.id}/offhours`

/** Answers the card's read with `body` and returns a function that tells whether the read has happened. */
function answerWith(body: ServiceOffHours) {
  let requested = false
  server.use(
    http.get(offHoursPath, () => {
      requested = true
      return HttpResponse.json(body)
    }),
  )
  return () => requested
}

describe('OffHoursCard', () => {
  it('is hidden when the service is not enrolled', async () => {
    const requested = answerWith(SERVICE_OFFHOURS_NONE)
    renderWithProviders(<OffHoursCard service={SERVICE_PAYMENTS} />)
    await waitFor(() => expect(requested()).toBe(true))
    // The read has answered; give React Query a turn to render that answer.
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(screen.queryByRole('region', { name: 'Off-hours' })).not.toBeInTheDocument()
  })

  it('asleep shows the schedule, the state and Wake for 2 hours', async () => {
    answerWith(SERVICE_OFFHOURS_ASLEEP)
    renderWithProviders(<OffHoursCard service={SERVICE_PAYMENTS} />)
    const card = await screen.findByRole('region', { name: 'Off-hours' })
    expect(within(card).getByText(`On "${SERVICE_OFFHOURS_ASLEEP.schedule_name}" (Europe/Berlin).`)).toBeInTheDocument()
    expect(within(card).getByText('Asleep until Mon 07:00')).toBeInTheDocument()
    expect(within(card).getByRole('button', { name: 'Wake for 2 hours' })).toBeEnabled()
    expect(within(card).queryByRole('button', { name: 'Put back to sleep' })).not.toBeInTheDocument()
  })

  it('woken shows Put back to sleep and not Wake', async () => {
    answerWith(SERVICE_OFFHOURS_WOKEN)
    renderWithProviders(<OffHoursCard service={SERVICE_PAYMENTS} />)
    const card = await screen.findByRole('region', { name: 'Off-hours' })
    expect(within(card).getByRole('button', { name: 'Put back to sleep' })).toBeEnabled()
    expect(within(card).queryByRole('button', { name: 'Wake for 2 hours' })).not.toBeInTheDocument()
  })

  it('without Operate there are no buttons and the card says why', async () => {
    answerWith({ ...SERVICE_OFFHOURS_ASLEEP, can_operate: false })
    renderWithProviders(<OffHoursCard service={SERVICE_PAYMENTS} />)
    const card = await screen.findByRole('region', { name: 'Off-hours' })
    expect(within(card).queryByRole('button')).not.toBeInTheDocument()
    expect(within(card).getByText(`You need Operate on ${SERVICE_PAYMENTS.tag.name} to wake it.`)).toBeInTheDocument()
  })

  it('blocked shows its reason and no buttons', async () => {
    answerWith(SERVICE_OFFHOURS_BLOCKED)
    renderWithProviders(<OffHoursCard service={SERVICE_PAYMENTS} />)
    const card = await screen.findByRole('region', { name: 'Off-hours' })
    expect(within(card).getByText(SERVICE_OFFHOURS_BLOCKED.reason)).toBeInTheDocument()
    expect(within(card).getByText('Blocked')).toBeInTheDocument()
    expect(within(card).queryByRole('button')).not.toBeInTheDocument()
  })
})
