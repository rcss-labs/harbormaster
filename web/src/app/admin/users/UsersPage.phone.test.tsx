import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { SSO_CONFIGURED_OFF } from '../../../test/fixtures'
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

const OMER_ID = 3
const ssoConfigured = () => http.get('/api/sso', () => HttpResponse.json(SSO_CONFIGURED_OFF))
/** The named item of a user's actions sheet; SSO items appear once the SSO configuration has loaded. */
const sheetItem = async (user: string, name: string | RegExp) => {
  const menu = await openActions(user)
  return waitFor(() => within(menu).getByRole('menuitem', { name }))
}
const userRow = async (name: string) => {
  const cell = await screen.findByText(name, { selector: '[data-hm-cell="username"] *, [data-hm-cell="username"]' })
  return cell.closest('[role=row]') as HTMLElement
}
const openActions = async (name: string) => {
  fireEvent.click(await screen.findByRole('button', { name: `Actions for ${name}` }))
  return screen.getByRole('menu', { name: `Actions for ${name}` })
}

describe('Users on a phone', () => {
  it('lists users as rows with role, sign-in methods, email and last sign-in instead of a matrix', async () => {
    renderApp('/settings/users')
    const sam = await userRow('sam')
    expect(within(sam).getByText('Global admin')).toBeInTheDocument()
    expect(within(sam).getByText('Password')).toBeInTheDocument()
    expect(within(sam).getByText('SSO linked')).toBeInTheDocument()
    expect(within(sam).getByText('sam@example.test')).toBeInTheDocument()
    expect(within(sam).getByText('(you)')).toBeInTheDocument()
    const omer = await userRow('omer')
    expect(within(omer).getByText('User')).toBeInTheDocument()
    // The wide matrix is gone: its per-tag buttons live in the permissions sheet.
    expect(screen.queryByRole('button', { name: 'omer on Prod: None' })).not.toBeInTheDocument()
  })

  it('the actions sheet keeps every desktop action and its disabled reason inside the row', async () => {
    renderApp('/settings/users')
    const menu = await openActions('sam')
    for (const name of ['Edit permissions', 'Reset password', 'Remove break-glass', 'Remove global admin', 'Delete']) {
      expect(within(menu).getByRole('menuitem', { name: new RegExp(`^${name}`) })).toBeInTheDocument()
    }
    const remove = within(menu).getByRole('menuitem', { name: /^Delete/ })
    expect(remove).toBeDisabled()
    expect(remove).toHaveTextContent('You cannot delete yourself')
  })

  it('shows why break-glass is disabled for a user without a password', async () => {
    renderApp('/settings/users')
    const menu = await openActions('alex')
    const item = within(menu).getByRole('menuitem', { name: /^Make break-glass/ })
    expect(item).toBeDisabled()
    expect(item).toHaveTextContent('Set a password for alex first.')
    expect(within(menu).getByRole('menuitem', { name: 'Set password' })).toBeInTheDocument()
  })

  it('Delete asks for confirmation with the desktop wording and then deletes', async () => {
    const calls: string[] = []
    server.use(
      http.delete('/api/users/3', () => {
        calls.push('delete')
        return new HttpResponse(null, { status: 204 })
      }),
    )
    renderApp('/settings/users')
    const menu = await openActions('omer')
    fireEvent.click(within(menu).getByRole('menuitem', { name: 'Delete' }))
    expect(screen.getByText('Delete omer?')).toBeInTheDocument()
    expect(screen.getByText('Their sessions end now. Their entries in operations and audit logs stay.')).toBeInTheDocument()
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Delete user' }))
    await waitFor(() => expect(calls).toEqual(['delete']))
  })

  it('Edit permissions lists every tag with its preset and edits one in a nested sheet', async () => {
    const bodies: unknown[] = []
    server.use(
      http.put(`/api/permissions/${OMER_ID}/3`, async ({ request }) => {
        bodies.push(await request.json())
        return HttpResponse.json({ actions: 7 })
      }),
    )
    renderApp('/settings/users')
    const menu = await openActions('omer')
    fireEvent.click(within(menu).getByRole('menuitem', { name: 'Edit permissions' }))
    const sheet = await screen.findByRole('dialog', { name: 'Edit permissions for omer' })
    expect(await within(sheet).findByRole('button', { name: 'omer on Dev: Operator' })).toHaveTextContent('VLO')
    expect(within(sheet).getByRole('button', { name: 'omer on Staging: Viewer' })).toBeInTheDocument()
    fireEvent.click(within(sheet).getByRole('button', { name: 'omer on Prod: None' }))
    fireEvent.change(screen.getByLabelText('Preset for omer on Prod'), { target: { value: 'Operator' } })
    await waitFor(() => expect(bodies).toEqual([{ actions: 7 }]))
  })

  it('a global admin has nothing to edit per tag', async () => {
    renderApp('/settings/users')
    const menu = await openActions('sam')
    fireEvent.click(within(menu).getByRole('menuitem', { name: 'Edit permissions' }))
    const sheet = await screen.findByRole('dialog', { name: 'Edit permissions for sam' })
    expect(within(sheet).getByText('Everything, on every tag')).toBeInTheDocument()
    expect(within(sheet).queryByRole('button', { name: /^sam on / })).not.toBeInTheDocument()
  })

  it('Add user opens a sheet', async () => {
    renderApp('/settings/users')
    fireEvent.click(await screen.findByRole('button', { name: 'Add user' }))
    const sheet = await screen.findByRole('dialog')
    expect(within(sheet).getByLabelText('Username')).toBeInTheDocument()
  })

  it('Link SSO… opens the NameID dialog and sets the pending link', async () => {
    const bodies: unknown[] = []
    server.use(
      ssoConfigured(),
      http.put('/api/users/3/sso-link', async ({ request }) => {
        bodies.push(await request.json())
        return new HttpResponse(null, { status: 204 })
      }),
    )
    renderApp('/settings/users')
    fireEvent.click(await sheetItem('omer', 'Link SSO…'))
    expect(screen.getByText('Link omer to an SSO identity')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('NameID'), { target: { value: 'omer@example.test' } })
    fireEvent.click(screen.getByRole('button', { name: 'Set pending link' }))
    await waitFor(() => expect(bodies).toEqual([{ name_id: 'omer@example.test' }]))
  })

  it('a pending link shows its NameID and expiry in the sheet and offers Change and Clear', async () => {
    const calls: string[] = []
    server.use(
      ssoConfigured(),
      http.delete('/api/users/2/sso-link', () => {
        calls.push('clear')
        return new HttpResponse(null, { status: 204 })
      }),
    )
    renderApp('/settings/users')
    const menu = await openActions('dana')
    // Not a tooltip: the NameID and the expiry are text in the sheet.
    expect(within(menu.closest('[role=dialog], [role=menu]') as HTMLElement).getByText(/Pending link to dana@example\.test, expires /)).toBeInTheDocument()
    fireEvent.click(await waitFor(() => within(menu).getByRole('menuitem', { name: 'Change pending link…' })))
    expect(screen.getByLabelText('NameID')).toHaveValue('dana@example.test')
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Cancel' }))
    const again = await openActions('dana')
    fireEvent.click(within(again).getByRole('menuitem', { name: 'Clear pending link' }))
    expect(await screen.findByText('Pending link cleared.')).toBeInTheDocument()
    expect(calls).toEqual(['clear'])
  })

  it('Unlink SSO… confirms with the desktop wording and unlinks', async () => {
    const calls: string[] = []
    server.use(
      http.post('/api/users/1/sso-unlink', () => {
        calls.push('unlink')
        return HttpResponse.json({})
      }),
    )
    renderApp('/settings/users')
    fireEvent.click(await sheetItem('sam', 'Unlink SSO…'))
    expect(screen.getByText('Unlink sam from SSO?')).toBeInTheDocument()
    expect(screen.getByText('Their SSO sessions end now. They keep their password.')).toBeInTheDocument()
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Unlink' }))
    await waitFor(() => expect(calls).toEqual(['unlink']))
  })

  it('Make global admin confirms with the desktop wording and posts admin true', async () => {
    const bodies: unknown[] = []
    server.use(
      http.post('/api/users/3/admin', async ({ request }) => {
        bodies.push(await request.json())
        return new HttpResponse(null, { status: 204 })
      }),
    )
    renderApp('/settings/users')
    fireEvent.click(await sheetItem('omer', 'Make global admin'))
    expect(screen.getByText('Make omer a global admin?')).toBeInTheDocument()
    expect(screen.getByText('Global admins can do everything on every tag, and manage users, tags and AWS accounts.')).toBeInTheDocument()
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Make global admin' }))
    await waitFor(() => expect(bodies).toEqual([{ admin: true }]))
  })

  it('the permissions sheet says how many services each tag covers', async () => {
    renderApp('/settings/users')
    const menu = await openActions('omer')
    fireEvent.click(within(menu).getByRole('menuitem', { name: 'Edit permissions' }))
    const sheet = await screen.findByRole('dialog', { name: 'Edit permissions for omer' })
    await within(sheet).findByRole('button', { name: 'omer on Dev: Operator' })
    expect(within(sheet).getAllByText(/^\d+ services$/)).toHaveLength(3)
  })
})
