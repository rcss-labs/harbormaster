import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import { AUTH_OPTIONS_ON, ME_ADMIN } from '../test/fixtures'
import { server } from '../test/msw'
import { renderApp } from '../test/renderApp'

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})

describe('router', () => {
  it('/services renders the Services page ahead of the service detail route', async () => {
    renderApp('/services')
    expect(await screen.findByRole('heading', { level: 1, name: 'Services' })).toBeInTheDocument()
    expect(await screen.findByRole('table', { name: 'Services' })).toBeInTheDocument()
  })

  it('/config renders the config search page', async () => {
    renderApp('/config')
    expect(await screen.findByRole('heading', { level: 1, name: 'Config search' })).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Search' })).toBeInTheDocument()
  })

  it('/logs renders the log search page', async () => {
    renderApp('/logs')
    expect(await screen.findByRole('heading', { level: 1, name: 'Log search' })).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Run query' })).toBeInTheDocument()
  })
})

describe('router: OAuth consent', () => {
  const unauthenticated = () =>
    HttpResponse.json({ error: { code: 'unauthenticated', message: 'Sign in required.' } }, { status: 401 })

  it('an anonymous visitor returns to /oauth/authorize with the query after signing in', async () => {
    const query = 'response_type=code&client_id=https%3A%2F%2Fclaude.ai%2Fc.json&state=a%20b&redirect_uri=https%3A%2F%2Fclaude.ai%2Fcb'
    let signedIn = false
    server.use(
      http.get('/api/me', () => (signedIn ? HttpResponse.json(ME_ADMIN) : unauthenticated())),
      http.post('/api/auth/login', () => {
        signedIn = true
        return HttpResponse.json(ME_ADMIN)
      }),
      http.get('/api/oauth/authorize', () => HttpResponse.json({ error: 'invalid_client', error_description: 'x' }, { status: 400 })),
    )
    renderApp(`/oauth/authorize?${query}`)
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Username'), 'sam')
    await user.type(screen.getByLabelText('Password'), 'correct horse battery')
    expect(screen.getByTestId('location')).toHaveTextContent('/login')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent('/oauth/authorize'))
    expect(screen.getByTestId('search')).toHaveTextContent(query)
  })

  it('the SSO button carries the consent link and its query as return_to', async () => {
    const query = 'response_type=code&state=s1'
    server.use(
      http.get('/api/me', unauthenticated),
      http.get('/api/auth/options', () => HttpResponse.json(AUTH_OPTIONS_ON)),
    )
    renderApp(`/oauth/authorize?${query}`)
    const link = await screen.findByRole('link', { name: 'Sign in with Okta' })
    const href = new URL(link.getAttribute('href') ?? '', 'http://x')
    expect(href.searchParams.get('return_to')).toBe(`/oauth/authorize?${query}`)
  })
})
