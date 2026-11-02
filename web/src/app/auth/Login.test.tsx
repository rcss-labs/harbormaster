import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { AUTH_OPTIONS_OFF, AUTH_OPTIONS_ON } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

const unauthenticated = () =>
  HttpResponse.json({ error: { code: 'unauthenticated', message: 'Sign in required.' } }, { status: 401 })

function useOptions(options: typeof AUTH_OPTIONS_OFF) {
  server.use(http.get('/api/me', unauthenticated), http.get('/api/auth/options', () => HttpResponse.json(options)))
}

describe('Login with single sign-on', () => {
  it('renders the password form when SSO is not configured', async () => {
    useOptions(AUTH_OPTIONS_OFF)
    renderApp('/login')
    expect(await screen.findByLabelText('Username')).toBeInTheDocument()
    expect(screen.getByLabelText('Password')).toBeInTheDocument()
    expect(screen.queryByText('Sign in with SSO')).not.toBeInTheDocument()
    expect(screen.queryByText('Sign in with a password instead')).not.toBeInTheDocument()
  })

  it('shows the SSO button and hides the form when SSO is on', async () => {
    useOptions({ sso: { enabled: true, label: 'Sign in with SSO' }, password_login: 'break_glass' })
    renderApp('/login')
    const button = await screen.findByRole('link', { name: 'Sign in with SSO' })
    expect(button.getAttribute('href')).toContain('/api/auth/saml/login?return_to=%2F')
    expect(screen.queryByLabelText('Username')).not.toBeInTheDocument()
    await userEvent.setup().click(screen.getByRole('button', { name: 'Sign in with a password instead' }))
    expect(screen.getByLabelText('Username')).toBeInTheDocument()
    expect(screen.getByText('Only break-glass accounts can sign in with a password.')).toBeInTheDocument()
  })

  it('uses a custom label', async () => {
    useOptions(AUTH_OPTIONS_ON)
    renderApp('/login')
    expect(await screen.findByRole('link', { name: 'Sign in with Okta' })).toBeInTheDocument()
  })

  it('sends the intended path as return_to', async () => {
    useOptions(AUTH_OPTIONS_ON)
    renderApp('/clusters')
    const button = await screen.findByRole('link', { name: 'Sign in with Okta' })
    expect(button.getAttribute('href')).toBe('/api/auth/saml/login?return_to=%2Fclusters')
  })

  it.each([
    ['sso_failed', 'Single sign-on failed. Try again, or ask a global admin to check the audit log.'],
    ['sso_declined', 'Your identity provider declined the sign-in.'],
    ['sso_off', 'Single sign-on is turned off.'],
  ])('shows the %s message from the query', async (code, message) => {
    useOptions(AUTH_OPTIONS_ON)
    renderApp(`/login?error=${code}`)
    expect(await screen.findByRole('alert')).toHaveTextContent(message)
  })

  it('shows the signed-out note only when SSO is on', async () => {
    const note = 'You are signed out of Harbormaster. To sign out of your identity provider, sign out there.'
    useOptions(AUTH_OPTIONS_ON)
    const first = renderApp('/login?signed_out=1')
    expect(await screen.findByText(note)).toBeInTheDocument()
    first.unmount()
    useOptions(AUTH_OPTIONS_OFF)
    renderApp('/login?signed_out=1')
    await screen.findByLabelText('Username')
    expect(screen.queryByText(note)).not.toBeInTheDocument()
  })

  it('shows sso_required from the API', async () => {
    useOptions(AUTH_OPTIONS_ON)
    server.use(
      http.post('/api/auth/login', () =>
        HttpResponse.json(
          { error: { code: 'sso_required', message: 'Use single sign-on to sign in to this account.' } },
          { status: 403 },
        ),
      ),
    )
    renderApp('/login')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Sign in with a password instead' }))
    await user.type(screen.getByLabelText('Username'), 'omer')
    await user.type(screen.getByLabelText('Password'), 'x')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(await screen.findByText('Use single sign-on to sign in to this account.')).toBeInTheDocument()
  })
})
