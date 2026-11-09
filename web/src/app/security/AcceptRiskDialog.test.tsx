import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { SECURITY_IMAGE_EDGE } from '../../test/securityFixtures'
import { server } from '../../test/msw'
import { renderWithProviders } from '../../test/setup'
import { SERVICE_NOTIFICATIONS, SERVICE_PAYMENTS } from '../../test/fixtures'
import { AcceptRiskDialog } from './AcceptRiskDialog'

const HIGH_FINDING = SECURITY_IMAGE_EDGE.findings[1]

function posted() {
  const bodies: unknown[] = []
  server.use(
    http.post('/api/security/images/:digest/acceptances', async ({ request }) => {
      bodies.push(await request.json())
      return HttpResponse.json({ ids: [9100] }, { status: 201 })
    }),
  )
  return bodies
}

const open = (requests = SECURITY_IMAGE_EDGE.requests, onClose = vi.fn()) =>
  renderWithProviders(<AcceptRiskDialog digest={SECURITY_IMAGE_EDGE.digest} target={{ kind: 'finding', finding: HIGH_FINDING }} requests={requests} onClose={onClose} />)

describe('AcceptRiskDialog', () => {
  it('AcceptRiskDialog lists only services the user can accept for and posts them', async () => {
    const bodies = posted()
    const onClose = vi.fn()
    open(undefined, onClose)
    const dialog = await screen.findByRole('dialog', { name: 'Accept risk for CVE-0000-0002' })
    expect(within(dialog).getByRole('checkbox', { name: 'payments-api' })).toBeChecked()
    expect(within(dialog).queryByRole('checkbox', { name: 'notifications' })).not.toBeInTheDocument()
    expect(within(dialog).getByText('Other services that use this image still count it.')).toBeInTheDocument()

    await userEvent.type(within(dialog).getByRole('textbox', { name: 'Reason' }), 'Not reachable')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Accept for 30 days' }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(await screen.findByText(/^CVE-0000-0002 accepted until .* for 1 service\. It returns to the list when that ends\.$/)).toBeInTheDocument()
    expect(bodies).toEqual([
      {
        vuln_id: 'CVE-0000-0002',
        package_name: 'libxml2',
        package_version: '2.9.14',
        reason: 'Not reachable',
        days: 30,
        service_ids: [SERVICE_PAYMENTS.id],
      },
    ])
  })

  it('leaves out services that already accepted the finding and posts only the others', async () => {
    const bodies = posted()
    const critical = SECURITY_IMAGE_EDGE.findings[0]
    const requests = SECURITY_IMAGE_EDGE.requests.map((request) => ({ ...request, can_accept: true }))
    renderWithProviders(<AcceptRiskDialog digest={SECURITY_IMAGE_EDGE.digest} target={{ kind: 'finding', finding: critical }} requests={requests} onClose={vi.fn()} />)
    const dialog = await screen.findByRole('dialog', { name: 'Accept risk for CVE-0000-0001' })
    expect(within(dialog).queryByRole('checkbox', { name: 'payments-api' })).not.toBeInTheDocument()
    expect(within(dialog).getByRole('checkbox', { name: 'notifications' })).toBeChecked()
    await userEvent.type(within(dialog).getByRole('textbox', { name: 'Reason' }), 'Fixed next week')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Accept for 30 days' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toMatchObject({ service_ids: [SERVICE_NOTIFICATIONS.id] })
  })

  it('says why when the server answers 409 and keeps the dialog open', async () => {
    server.use(
      http.post('/api/security/images/:digest/acceptances', () =>
        HttpResponse.json({ error: { code: 'conflict', message: 'That finding is already accepted for one of these services.' } }, { status: 409 }),
      ),
    )
    const onClose = vi.fn()
    open(undefined, onClose)
    const dialog = await screen.findByRole('dialog', { name: 'Accept risk for CVE-0000-0002' })
    await userEvent.type(within(dialog).getByRole('textbox', { name: 'Reason' }), 'Not reachable')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Accept for 30 days' }))
    expect(await screen.findByText('That finding is already accepted for one of these services.')).toBeInTheDocument()
    expect(onClose).not.toHaveBeenCalled()
  })

  it('uses the singular for one day and the plural for several services', async () => {
    posted()
    const requests = SECURITY_IMAGE_EDGE.requests.map((request) => ({ ...request, can_accept: true }))
    open(requests)
    const dialog = await screen.findByRole('dialog', { name: 'Accept risk for CVE-0000-0002' })
    await userEvent.type(within(dialog).getByRole('textbox', { name: 'Reason' }), 'Ok')
    const days = within(dialog).getByRole('spinbutton', { name: 'Days' })
    await userEvent.clear(days)
    await userEvent.type(days, '1')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Accept for 1 day' }))
    expect(await screen.findByText(/for 2 services\./)).toBeInTheDocument()
  })

  it('AcceptRiskDialog requires a reason and posts days', async () => {
    const bodies = posted()
    const requests = SECURITY_IMAGE_EDGE.requests.map((request) => ({ ...request, can_accept: true }))
    open(requests)
    const dialog = await screen.findByRole('dialog', { name: 'Accept risk for CVE-0000-0002' })
    const accept = () => within(dialog).getByRole('button', { name: /^Accept for \d+ days$/ })
    expect(accept()).toBeDisabled()

    await userEvent.type(within(dialog).getByRole('textbox', { name: 'Reason' }), 'Patch next sprint')
    const days = within(dialog).getByRole('spinbutton', { name: 'Days' })
    await userEvent.clear(days)
    await userEvent.type(days, '7')
    await userEvent.click(within(dialog).getByRole('checkbox', { name: 'notifications' }))
    await userEvent.click(within(dialog).getByRole('button', { name: 'Accept for 7 days' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0]).toMatchObject({ days: 7, reason: 'Patch next sprint', service_ids: [SERVICE_PAYMENTS.id] })
    expect(SERVICE_NOTIFICATIONS.id).not.toBe(SERVICE_PAYMENTS.id)
  })
})
