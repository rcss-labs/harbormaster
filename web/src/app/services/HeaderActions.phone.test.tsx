import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SERVICE_PAYMENTS } from '../../test/fixtures'
import { server } from '../../test/msw'
import { installPhoneMedia } from '../../test/phone'
import { renderWithProviders } from '../../test/setup'
import { DesiredCountSheet } from './HeaderActions'

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
})
afterEach(() => restoreMedia())

const info = () =>
  http.get(`/api/services/${SERVICE_PAYMENTS.id}/desired-count`, () =>
    HttpResponse.json({ desired: SERVICE_PAYMENTS.desired, running: SERVICE_PAYMENTS.running, max: 50, auto_scaling: { min: 2, max: 4 } }),
  )

describe('DesiredCountSheet on phones', () => {
  it('desired count sheet → confirmation sheet keeps the dialog strings', async () => {
    const posted = vi.fn()
    server.use(
      info(),
      http.post(`/api/services/${SERVICE_PAYMENTS.id}/desired-count`, async ({ request }) => {
        posted(await request.json())
        return HttpResponse.json({ desired: 0, previous: SERVICE_PAYMENTS.desired, changed: true })
      }),
    )
    renderWithProviders(<DesiredCountSheet serviceId={SERVICE_PAYMENTS.id} service={SERVICE_PAYMENTS} onClose={() => {}} />)
    const user = userEvent.setup()

    const sheet = screen.getByRole('dialog', { name: 'Change desired count' })
    expect(sheet).toHaveTextContent(`Now ${SERVICE_PAYMENTS.desired}.`)
    // Nothing to apply until the count differs.
    const update = within(sheet).getByRole('button', { name: 'Update desired count' })
    expect(update).toBeDisabled()

    const field = within(sheet).getByRole('spinbutton', { name: 'Desired count' })
    await user.clear(field)
    await user.type(field, '0')
    expect(await within(sheet).findByText(/Auto scaling keeps this service between 2 and 4/)).toBeInTheDocument()
    await user.click(update)

    const confirm = screen.getByRole('dialog', { name: `Set ${SERVICE_PAYMENTS.name} to 0 tasks?` })
    expect(confirm).toHaveTextContent(`Now ${SERVICE_PAYMENTS.desired}. Every task stops. The service and its definition stay.`)
    expect(posted).not.toHaveBeenCalled()
    await user.click(within(confirm).getByRole('button', { name: 'Update desired count' }))
    await waitFor(() => expect(posted).toHaveBeenCalledWith({ desired: 0 }))
    expect(await screen.findByText('Desired count set to 0.')).toBeInTheDocument()
  })
})
