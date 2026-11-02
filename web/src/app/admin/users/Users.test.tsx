import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { server } from '../../../test/msw'
import { AUTH_OPTIONS_ON, ME_ADMIN, ME_SSO_USER, SSO_CONFIGURED_OFF, USERS } from '../../../test/fixtures'
import { renderApp } from '../../../test/renderApp'
import { PRESETS, normalize, presetOf, toggle } from '../perms'

const OMER_ID = 3
const PROD_TAG_ID = 3

describe('perms', () => {
  it('presetOf and normalize tables (Viewer=1, Operator=7, Deployer=31, Full=63, 34→Custom, toggle view off clears)', () => {
    expect(PRESETS).toEqual({ None: 0, Viewer: 1, Operator: 7, Deployer: 31, Full: 63 })
    expect([0, 1, 7, 31, 63].map(presetOf)).toEqual(['None', 'Viewer', 'Operator', 'Deployer', 'Full'])
    expect(presetOf(34)).toBe('Custom')
    expect(normalize(34)).toBe(35)
    expect(normalize(2)).toBe(3)
    expect(normalize(0)).toBe(0)
    expect(toggle(1, 2)).toBe(3)
    expect(toggle(0, 8)).toBe(9)
    expect(toggle(7, 4)).toBe(3)
    expect(toggle(63, 1)).toBe(0)
  })
})

describe('matrix', () => {
  const omerRow = async () => {
    const cell = await screen.findByText('omer', { selector: 'td div' })
    return cell.closest('tr') as HTMLElement
  }

  it('clicking L on omer/Prod PUTs actions 3', async () => {
    const bodies: unknown[] = []
    server.use(
      http.put(`/api/permissions/${OMER_ID}/${PROD_TAG_ID}`, async ({ request }) => {
        bodies.push(await request.json())
        return HttpResponse.json({ actions: 3 })
      }),
    )
    renderApp('/settings/users')
    expect(await screen.findByRole('heading', { level: 2, name: 'Users & permissions' })).toBeInTheDocument()
    const row = await omerRow()
    // Columns are Dev, Staging, Prod: Prod is the third cell.
    fireEvent.click(within(row).getAllByTitle('Logs')[2])
    await waitFor(() => expect(bodies).toEqual([{ actions: 3 }]))
  })

  it('choosing preset Operator PUTs 7', async () => {
    const bodies: unknown[] = []
    server.use(
      http.put(`/api/permissions/${OMER_ID}/${PROD_TAG_ID}`, async ({ request }) => {
        bodies.push(await request.json())
        return HttpResponse.json({ actions: 7 })
      }),
    )
    renderApp('/settings/users')
    await omerRow()
    fireEvent.change(screen.getByLabelText('Preset for omer on Prod'), { target: { value: 'Operator' } })
    await waitFor(() => expect(bodies).toEqual([{ actions: 7 }]))
  })
})

describe('users', () => {
  it('Delete disabled for current user', async () => {
    renderApp('/settings/users')
    const you = await screen.findByText('(you)')
    const row = you.closest('tr') as HTMLElement
    expect(within(row).getByRole('button', { name: 'Delete' })).toBeDisabled()
    expect(within(row).getByRole('button', { name: 'Delete' })).toHaveAttribute('title', 'You cannot delete yourself')
    const other = screen.getAllByText('dana', { selector: 'td' })[0].closest('tr') as HTMLElement
    expect(within(other).getByRole('button', { name: 'Delete' })).toBeEnabled()
  })
})

describe('add user', () => {
  it('shows temporary password once', async () => {
    server.use(
      http.post('/api/users', async ({ request }) => {
        expect(await request.json()).toEqual({ username: 'alex', admin: false })
        return HttpResponse.json({ user: { ...USERS.items[2], id: 9, username: 'alex' }, temporary_password: 'abcd-1234-ef56' }, { status: 201 })
      }),
    )
    renderApp('/settings/users')
    fireEvent.click(await screen.findByRole('button', { name: 'Add user' }))
    fireEvent.change(screen.getByLabelText('Username'), { target: { value: 'alex' } })
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Add user' }))
    expect(await screen.findByText('Temporary password for alex')).toBeInTheDocument()
    expect(screen.getByText('abcd-1234-ef56')).toBeInTheDocument()
    expect(screen.getByText('Shown once. Copy it now.')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Done' }))
    expect(screen.queryByText('abcd-1234-ef56')).not.toBeInTheDocument()
  })
})

const userRow = async (name: string) => {
  const cell = (await screen.findAllByText(name, { selector: 'td' }))[0]
  return cell.closest('tr') as HTMLElement
}
const capture = (method: 'post' | 'put' | 'delete', path: string, bodies: unknown[], status = 204) =>
  http[method](path, async ({ request }) => {
    bodies.push(method === 'delete' ? null : await request.json())
    return new HttpResponse(null, { status })
  })
const ssoConfigured = () => http.get('/api/sso', () => HttpResponse.json(SSO_CONFIGURED_OFF))

describe('sign-in credentials', () => {
  it('shows Password, SSO linked, Link pending, Link expired and Break-glass pills and the email', async () => {
    renderApp('/settings/users')
    const pavel = await userRow('pavel')
    expect(within(pavel).getByText('Password')).toBeInTheDocument()
    expect(within(pavel).getByText('SSO linked')).toBeInTheDocument()
    expect(within(pavel).getByText('Break-glass')).toBeInTheDocument()
    expect(within(pavel).getByText('pavel@example.test')).toBeInTheDocument()
    const dana = await userRow('dana')
    expect(within(dana).getByText('Link pending')).toHaveAttribute('title', expect.stringContaining('Pending link to dana@example.test, expires '))
    expect(within(await userRow('oncall')).getByText('Link expired')).toBeInTheDocument()
    expect(within(await userRow('alex')).queryByText('Password')).not.toBeInTheDocument()
  })

  it('labels Reset password as Set password for password-less users', async () => {
    renderApp('/settings/users')
    const alex = await userRow('alex')
    fireEvent.click(within(alex).getByRole('button', { name: 'Set password' }))
    expect(
      screen.getByText('They get a temporary password and must choose their own at next sign-in. While single sign-on is on, only break-glass accounts can use it.'),
    ).toBeInTheDocument()
    expect(within(await userRow('omer')).getByRole('button', { name: 'Reset password' })).toBeInTheDocument()
  })

  it('disables Make break-glass for password-less users with the title', async () => {
    renderApp('/settings/users')
    const button = within(await userRow('alex')).getByRole('button', { name: 'Make break-glass' })
    expect(button).toBeDisabled()
    expect(button).toHaveAttribute('title', 'Set a password for alex first.')
    expect(within(await userRow('omer')).getByRole('button', { name: 'Make break-glass' })).toBeEnabled()
  })

  it('break-glass confirm posts {break_glass:true} and refreshes', async () => {
    const bodies: unknown[] = []
    let listCalls = 0
    server.use(
      capture('post', '/api/users/3/break-glass', bodies),
      http.get('/api/users', () => {
        listCalls++
        return HttpResponse.json(USERS)
      }),
    )
    renderApp('/settings/users')
    fireEvent.click(within(await userRow('omer')).getByRole('button', { name: 'Make break-glass' }))
    expect(screen.getByText('Make omer a break-glass account?')).toBeInTheDocument()
    expect(
      screen.getByText('Break-glass accounts can sign in with a password even when single sign-on is on. Keep this to a few global admins.'),
    ).toBeInTheDocument()
    const before = listCalls
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Make break-glass' }))
    await waitFor(() => expect(bodies).toEqual([{ break_glass: true }]))
    await waitFor(() => expect(listCalls).toBeGreaterThan(before))
  })

  it('removing break-glass confirms and surfaces the 422 message', async () => {
    server.use(
      http.post('/api/users/1/break-glass', () =>
        HttpResponse.json({ error: { code: 'invalid', message: 'Keep at least one break-glass global admin while single sign-on is on.' } }, { status: 422 }),
      ),
    )
    renderApp('/settings/users')
    fireEvent.click(within(await userRow('pavel')).getByRole('button', { name: 'Remove break-glass' }))
    expect(screen.getByText('Remove break-glass from pavel?')).toBeInTheDocument()
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Remove break-glass' }))
    expect(await screen.findByText('Keep at least one break-glass global admin while single sign-on is on.')).toBeInTheDocument()
  })

  it('pending link dialog puts {name_id} and shows Link pending', async () => {
    const bodies: unknown[] = []
    server.use(ssoConfigured(), capture('put', '/api/users/3/sso-link', bodies))
    renderApp('/settings/users')
    fireEvent.click(await within(await userRow('omer')).findByRole('button', { name: 'Link SSO…' }))
    expect(screen.getByText('Link omer to an SSO identity')).toBeInTheDocument()
    expect(screen.getByText('Copy it exactly as the identity provider issues it. Test sign-in shows it.')).toBeInTheDocument()
    expect(
      screen.getByText('At their next SSO sign-in with this NameID, omer keeps this account and its permissions instead of getting a new one. The link expires in 14 days.'),
    ).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('NameID'), { target: { value: ' omer@example.test ' } })
    server.use(
      http.get('/api/users', () =>
        HttpResponse.json({
          items: USERS.items.map((u) => (u.id === 3 ? { ...u, sso: 'pending', sso_name_id: 'omer@example.test', pending_link_expires_at: '2099-01-01T00:00:00Z' } : u)),
        }),
      ),
    )
    fireEvent.click(screen.getByRole('button', { name: 'Set pending link' }))
    await waitFor(() => expect(bodies).toEqual([{ name_id: 'omer@example.test' }]))
    await waitFor(() => expect(within(screen.getAllByText('omer', { selector: 'td' })[0].closest('tr') as HTMLElement).getByText('Link pending')).toBeInTheDocument())
  })

  it('shows a conflict from the pending link request in the dialog', async () => {
    server.use(
      ssoConfigured(),
      http.put('/api/users/3/sso-link', () =>
        HttpResponse.json({ error: { code: 'sso_identity_taken', message: 'That NameID is already linked to another user.' } }, { status: 409 }),
      ),
    )
    renderApp('/settings/users')
    fireEvent.click(await within(await userRow('omer')).findByRole('button', { name: 'Link SSO…' }))
    fireEvent.change(screen.getByLabelText('NameID'), { target: { value: 'x' } })
    fireEvent.click(screen.getByRole('button', { name: 'Set pending link' }))
    expect(await within(screen.getByRole('dialog')).findByText('That NameID is already linked to another user.')).toBeInTheDocument()
  })

  it('offers Change pending link and Clear pending link for pending users; clear deletes and toasts', async () => {
    const bodies: unknown[] = []
    server.use(ssoConfigured(), capture('delete', '/api/users/2/sso-link', bodies))
    renderApp('/settings/users')
    const dana = await userRow('dana')
    expect(await within(dana).findByRole('button', { name: 'Change pending link…' })).toBeInTheDocument()
    fireEvent.click(within(dana).getByRole('button', { name: 'Clear pending link' }))
    expect(await screen.findByText('Pending link cleared.')).toBeInTheDocument()
    expect(bodies).toHaveLength(1)
  })

  it('unlink confirm differs by has_password and shows the temporary password', async () => {
    renderApp('/settings/users')
    fireEvent.click(within(await userRow('pavel')).getByRole('button', { name: 'Unlink SSO…' }))
    expect(screen.getByText('Unlink pavel from SSO?')).toBeInTheDocument()
    expect(screen.getByText('Their SSO sessions end now. They keep their password.')).toBeInTheDocument()
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Cancel' }))

    fireEvent.click(within(await userRow('alex')).getByRole('button', { name: 'Unlink SSO…' }))
    expect(
      screen.getByText('Their SSO sessions end now. They get a temporary password and must choose their own at next sign-in.'),
    ).toBeInTheDocument()
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Unlink' }))
    expect(await screen.findByText('tmp-pass-1234')).toBeInTheDocument()
  })

  it('subtitle changes when SSO is on', async () => {
    server.use(http.get('/api/auth/options', () => HttpResponse.json(AUTH_OPTIONS_ON)))
    renderApp('/settings/users')
    expect(
      await screen.findByText('Global admins can do everything. Every other user gets permissions per tag. Sign-in is single sign-on; break-glass accounts can also use a password.'),
    ).toBeInTheDocument()
  })

  it('subtitle stays username and password when SSO is off', async () => {
    renderApp('/settings/users')
    expect(
      await screen.findByText('Global admins can do everything. Every other user gets permissions per tag. Sign-in is username and password.'),
    ).toBeInTheDocument()
  })

  it('add user hint appears when SSO is on', async () => {
    server.use(http.get('/api/auth/options', () => HttpResponse.json(AUTH_OPTIONS_ON)))
    renderApp('/settings/users')
    await screen.findByText(/Sign-in is single sign-on/)
    fireEvent.click(screen.getByRole('button', { name: 'Add user' }))
    expect(
      await screen.findByText('New local users can only sign in with a password if you make them break-glass. SSO users are created automatically at their first sign-in, or set a pending link to attach one to an existing account.'),
    ).toBeInTheDocument()
  })
})

describe('change password route', () => {
  it('/password redirects home for a password-less user', async () => {
    server.use(http.get('/api/me', () => HttpResponse.json(ME_SSO_USER)))
    renderApp('/password')
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent(/^\/$/))
  })
})

describe('acting on yourself', () => {
  const serveMe = (initial: typeof ME_ADMIN) => {
    let current = initial
    server.use(
      http.get('/api/me', () => HttpResponse.json(current)),
      http.get('/api/auth/options', () => HttpResponse.json(AUTH_OPTIONS_ON)),
    )
    return (next: typeof ME_ADMIN) => {
      current = next
    }
  }

  it('unlinking yourself refreshes /api/me so the sidebar offers Link SSO account', async () => {
    const setMe = serveMe({ ...ME_ADMIN, sso_linked: true })
    server.use(
      http.post('/api/users/1/sso-unlink', () => {
        setMe({ ...ME_ADMIN, sso_linked: false })
        return HttpResponse.json({})
      }),
    )
    renderApp('/settings/users')
    await screen.findByRole('button', { name: 'Sign out' })
    expect(screen.queryByRole('button', { name: 'Link SSO account' })).not.toBeInTheDocument()
    fireEvent.click(within(await userRow('pavel')).getByRole('button', { name: 'Unlink SSO…' }))
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Unlink' }))
    expect(await screen.findByRole('button', { name: 'Link SSO account' })).toBeInTheDocument()
  })

  it('toggling break-glass refetches the SSO configuration', async () => {
    serveMe(ME_ADMIN)
    let configFetches = 0
    server.use(
      http.get('/api/sso', () => {
        configFetches += 1
        return HttpResponse.json(SSO_CONFIGURED_OFF)
      }),
    )
    renderApp('/settings/users')
    fireEvent.click(within(await userRow('pavel')).getByRole('button', { name: /break-glass/ }))
    await waitFor(() => expect(configFetches).toBeGreaterThan(0))
    const before = configFetches
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: /break-glass/ }))
    await waitFor(() => expect(configFetches).toBeGreaterThan(before))
  })
})
