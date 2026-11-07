import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { AUTH_OPTIONS_OFF, AUTH_OPTIONS_ON } from '../../test/fixtures'
import { server } from '../../test/msw'
import { installPhoneMedia } from '../../test/phone'
import { renderApp } from '../../test/renderApp'

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
})
afterEach(() => restoreMedia())

const unauthenticated = () => HttpResponse.json({ error: { code: 'unauthenticated', message: 'Sign in required.' } }, { status: 401 })
const useOptions = (options: typeof AUTH_OPTIONS_OFF) =>
  server.use(http.get('/api/me', unauthenticated), http.get('/api/auth/options', () => HttpResponse.json(options)))

describe('Login on a phone', () => {
  it('full-width form: the password form keeps its strings and stops the keyboard capitalising the username', async () => {
    useOptions(AUTH_OPTIONS_OFF)
    renderApp('/login')
    const username = await screen.findByLabelText('Username')
    expect(username).toHaveAttribute('autocapitalize', 'none')
    expect(username).toHaveAttribute('spellcheck', 'false')
    expect(screen.getByLabelText('Password')).toHaveAttribute('autocomplete', 'current-password')
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument()
    expect(screen.getByText('ECS control center')).toBeInTheDocument()
  })

  it('SSO mode strings are unchanged', async () => {
    useOptions(AUTH_OPTIONS_ON)
    renderApp('/login')
    expect(await screen.findByRole('link', { name: 'Sign in with Okta' })).toBeInTheDocument()
    await userEvent.setup().click(screen.getByRole('button', { name: 'Sign in with a password instead' }))
    expect(screen.getByText('Only break-glass accounts can sign in with a password.')).toBeInTheDocument()
  })
})
