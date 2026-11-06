import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { ACCOUNTS } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { installPhoneMedia } from '../../../test/phone'
import { renderApp } from '../../../test/renderApp'

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
})
afterEach(async () => {
  restoreMedia()
  await new Promise((resolve) => setTimeout(resolve, 50))
})

describe('Accounts on a phone', () => {
  it('lists accounts as rows with id, regions, clusters and status', async () => {
    renderApp('/settings/accounts')
    const name = await screen.findByText('homelab-admin')
    const row = name.closest('[role=row]') as HTMLElement
    expect(within(row).getByText('482109375512')).toBeInTheDocument()
    expect(within(row).getByText('Connected')).toBeInTheDocument()
    expect(within(row).getByText(/us-east-1, eu-west-1, eu-central-1/)).toBeInTheDocument()
  })

  it('the actions sheet has Test now, Edit, Disconnect and Details', async () => {
    renderApp('/settings/accounts')
    fireEvent.click(await screen.findByRole('button', { name: 'Actions for homelab-admin' }))
    const menu = screen.getByRole('menu', { name: 'Actions for homelab-admin' })
    for (const label of ['Test now', 'Edit', 'Disconnect', 'Details']) {
      expect(within(menu).getByRole('menuitem', { name: label })).toBeInTheDocument()
    }
  })

  it('Disconnect confirms with the desktop wording', async () => {
    renderApp('/settings/accounts')
    fireEvent.click(await screen.findByRole('button', { name: 'Actions for homelab-admin' }))
    fireEvent.click(within(screen.getByRole('menu')).getByRole('menuitem', { name: 'Disconnect' }))
    expect(
      await screen.findByText('Disconnect homelab-admin? Its clusters, services and tasks disappear from Harbormaster; tags and permissions are kept for 30 days.'),
    ).toBeInTheDocument()
  })

  it('Details shows the AWS error of a failing account', async () => {
    server.use(
      http.get('/api/accounts', () =>
        HttpResponse.json({ ...ACCOUNTS, items: [{ ...ACCOUNTS.items[0], status: 'error', last_error: 'AccessDenied: not authorized to assume the role' }] }),
      ),
    )
    renderApp('/settings/accounts')
    fireEvent.click(await screen.findByRole('button', { name: 'Actions for homelab-admin' }))
    fireEvent.click(within(screen.getByRole('menu')).getByRole('menuitem', { name: 'Details' }))
    const sheet = await screen.findByRole('dialog', { name: 'Details for homelab-admin' })
    expect(within(sheet).getByText('AccessDenied: not authorized to assume the role')).toBeInTheDocument()
    expect(within(sheet).getByText(/arn:aws:iam::482109375512:role\/harbormaster-access/)).toBeInTheDocument()
  })

  it('Test now posts the account and Connect account opens a sheet', async () => {
    const bodies: unknown[] = []
    server.use(
      http.post('/api/accounts/test', async ({ request }) => {
        bodies.push(await request.json())
        return HttpResponse.json({ regions: [{ region: 'us-east-1', ok: true, clusters: 2, error: '' }] })
      }),
    )
    renderApp('/settings/accounts')
    fireEvent.click(await screen.findByRole('button', { name: 'Actions for homelab-admin' }))
    fireEvent.click(within(screen.getByRole('menu')).getByRole('menuitem', { name: 'Test now' }))
    await waitFor(() => expect(bodies).toHaveLength(1))
    fireEvent.click(await screen.findByRole('button', { name: 'Connect account' }))
    expect(await screen.findByLabelText('Role ARN')).toBeInTheDocument()
  })
})
