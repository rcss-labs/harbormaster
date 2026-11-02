import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { MeDTO } from '../../api/types'
import { AUTH_OPTIONS_ON, ME_ADMIN, ME_SSO_USER } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'
import { navigateExternal } from './navigateExternal'

vi.mock('./navigateExternal', () => ({ navigateExternal: vi.fn() }))

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
  vi.mocked(navigateExternal).mockClear()
})

afterEach(async () => {
  await new Promise((resolve) => setTimeout(resolve, 100))
})

const ssoOn = () => server.use(http.get('/api/auth/options', () => HttpResponse.json(AUTH_OPTIONS_ON)))
const meIs = (me: MeDTO) => server.use(http.get('/api/me', () => HttpResponse.json(me)))
const LINK = 'Link SSO account'

describe('Sidebar SSO linking', () => {
  it('hides Change password for password-less users', async () => {
    meIs(ME_SSO_USER)
    renderApp('/')
    await screen.findByRole('button', { name: 'Sign out' })
    expect(screen.queryByRole('button', { name: 'Change password' })).not.toBeInTheDocument()
  })

  it('shows Link SSO account only when SSO is configured, the user has a password and is not linked', async () => {
    // SSO not configured (default handler): hidden.
    const first = renderApp('/')
    await screen.findByRole('button', { name: 'Sign out' })
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(screen.queryByRole('button', { name: LINK })).not.toBeInTheDocument()
    first.unmount()

    // Configured, password, not linked: shown.
    ssoOn()
    const second = renderApp('/')
    expect(await screen.findByRole('button', { name: LINK })).toBeInTheDocument()
    second.unmount()

    // Configured but already linked: hidden.
    meIs({ ...ME_ADMIN, sso_linked: true })
    renderApp('/')
    await screen.findByRole('button', { name: 'Sign out' })
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(screen.queryByRole('button', { name: LINK })).not.toBeInTheDocument()
  })

  it('dialog posts the password and navigates to redirect_url', async () => {
    ssoOn()
    let body: unknown
    server.use(
      http.post('/api/auth/saml/link', async ({ request }) => {
        body = await request.json()
        return HttpResponse.json({ redirect_url: 'https://idp.example/sso?x' })
      }),
    )
    renderApp('/')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: LINK }))
    const dialog = screen.getByRole('dialog', { name: 'Link your SSO account' })
    await user.type(within(dialog).getByLabelText('Password'), 'hunter2')
    await user.click(within(dialog).getByRole('button', { name: 'Continue to identity provider' }))
    await waitFor(() => expect(navigateExternal).toHaveBeenCalledWith('https://idp.example/sso?x'))
    expect(body).toEqual({ password: 'hunter2' })
  })

  it('dialog shows the API error for a wrong password', async () => {
    ssoOn()
    server.use(
      http.post('/api/auth/saml/link', () =>
        HttpResponse.json({ error: { code: 'unauthenticated', message: 'Current password is wrong.' } }, { status: 401 }),
      ),
    )
    renderApp('/')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: LINK }))
    const dialog = screen.getByRole('dialog')
    await user.type(within(dialog).getByLabelText('Password'), 'nope')
    await user.click(within(dialog).getByRole('button', { name: 'Continue to identity provider' }))
    expect(await within(dialog).findByText('Current password is wrong.')).toBeInTheDocument()
    expect(navigateExternal).not.toHaveBeenCalled()
  })
})

describe('Shell sso_link toasts', () => {
  it('sso_link=ok shows the toast, refreshes /api/me and strips the param', async () => {
    let meCalls = 0
    server.use(
      http.get('/api/me', () => {
        meCalls += 1
        return HttpResponse.json(ME_ADMIN)
      }),
    )
    renderApp('/?sso_link=ok')
    expect(await screen.findByText('Your SSO account is linked.')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent(/^$/))
    await waitFor(() => expect(meCalls).toBeGreaterThanOrEqual(2))
  })

  it('sso_link=sso_identity_taken shows the taken message', async () => {
    renderApp('/?sso_link=sso_identity_taken')
    expect(await screen.findByText('That SSO identity is already linked to another user.')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent(/^$/))
  })

  it('any other sso_link value shows the generic message', async () => {
    renderApp('/?sso_link=link_user_gone')
    expect(await screen.findByText('Linking failed. Ask a global admin to check the audit log.')).toBeInTheDocument()
  })
})
