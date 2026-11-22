import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ME_OMER } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

const QUERY = new URLSearchParams({
  response_type: 'code',
  client_id: 'https://claude.ai/oauth/mcp-client.json',
  redirect_uri: 'https://claude.ai/api/mcp/auth_callback',
  code_challenge: 'x'.repeat(43),
  code_challenge_method: 'S256',
  scope: 'harbormaster:read',
  state: 'st4te',
  resource: 'https://mcp.example.com/mcp',
}).toString()

const VIEW = {
  client_name: 'Claude',
  client_host: 'claude.ai',
  redirect_host: 'claude.ai',
  scope: 'harbormaster:read',
  read_only: true,
  expires_in_days: 7,
  sees_logs: false,
  username: 'omer',
}

const CALLBACK = 'https://claude.ai/api/mcp/auth_callback?code=abc&state=st4te&iss=https%3A%2F%2Fmcp.example.com'

let assign: ReturnType<typeof vi.fn>
const realLocation = window.location

beforeEach(() => {
  assign = vi.fn()
  Object.defineProperty(window, 'location', { configurable: true, value: { ...realLocation, assign } })
  server.use(http.get('/api/me', () => HttpResponse.json(ME_OMER)))
})

afterEach(() => {
  Object.defineProperty(window, 'location', { configurable: true, value: realLocation })
})

function serveView(view: Record<string, unknown> = VIEW) {
  let seenQuery = ''
  server.use(
    http.get('/api/oauth/authorize', ({ request }) => {
      seenQuery = new URL(request.url).search
      return HttpResponse.json(view)
    }),
  )
  return () => seenQuery
}

describe('OAuthConsent', () => {
  it('renders the consent card from the validate answer', async () => {
    const seen = serveView()
    renderApp(`/oauth/authorize?${QUERY}`)
    expect(await screen.findByRole('heading', { name: 'Connect Claude?' })).toBeInTheDocument()
    expect(screen.getByText(/Claude \(claude\.ai\) asks to use Harbormaster/)).toBeInTheDocument()
    expect(screen.getByText('Read-only access to Harbormaster as omer.')).toBeInTheDocument()
    expect(screen.getByText(/It will see everything you can see\./)).toBeInTheDocument()
    expect(screen.queryByText(/including logs/)).not.toBeInTheDocument()
    expect(screen.getByText(/Access lasts up to 7 days; you can revoke it on My access/)).toBeInTheDocument()
    expect(screen.getByText(/you return to claude\.ai/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Approve' })).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Deny' })).toBeEnabled()
    // The whole request goes to the server, as it was in the URL.
    const sent = new URLSearchParams(seen())
    expect(sent.get('redirect_uri')).toBe('https://claude.ai/api/mcp/auth_callback')
    expect(sent.get('code_challenge_method')).toBe('S256')
    expect(sent.get('state')).toBe('st4te')
  })

  it('mentions logs only when the user sees them', async () => {
    serveView({ ...VIEW, sees_logs: true, expires_in_days: 1 })
    renderApp(`/oauth/authorize?${QUERY}`)
    expect(await screen.findByText(/It will see everything you can see, including logs\./)).toBeInTheDocument()
    expect(screen.getByText(/Access lasts up to 1 day;/)).toBeInTheDocument()
  })

  it('approve posts the decision and navigates to redirect_to', async () => {
    serveView()
    let body: { decision?: string; request?: Record<string, string> } = {}
    server.use(
      http.post('/api/oauth/authorize', async ({ request }) => {
        body = (await request.json()) as typeof body
        return HttpResponse.json({ redirect_to: CALLBACK })
      }),
    )
    renderApp(`/oauth/authorize?${QUERY}`)
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(assign).toHaveBeenCalledWith(CALLBACK))
    expect(body.decision).toBe('approve')
    expect(body.request?.client_id).toBe('https://claude.ai/oauth/mcp-client.json')
    expect(body.request?.redirect_uri).toBe('https://claude.ai/api/mcp/auth_callback')
    expect(body.request?.code_challenge).toBe('x'.repeat(43))
    expect(body.request?.state).toBe('st4te')
  })

  it('deny posts the decision and navigates to redirect_to', async () => {
    serveView()
    let decision = ''
    const denied = 'https://claude.ai/api/mcp/auth_callback?error=access_denied&state=st4te'
    server.use(
      http.post('/api/oauth/authorize', async ({ request }) => {
        decision = ((await request.json()) as { decision: string }).decision
        return HttpResponse.json({ redirect_to: denied })
      }),
    )
    renderApp(`/oauth/authorize?${QUERY}`)
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Deny' }))
    await waitFor(() => expect(assign).toHaveBeenCalledWith(denied))
    expect(decision).toBe('deny')
  })

  it('an unredirectable error shows the message and no buttons', async () => {
    server.use(
      http.get('/api/oauth/authorize', () =>
        HttpResponse.json(
          { error: 'invalid_request', error_description: 'The application asked to return to an address it may not use.' },
          { status: 400 },
        ),
      ),
    )
    renderApp(`/oauth/authorize?${QUERY.replace('claude.ai%2Fapi', 'evil.example%2Fapi')}`)
    expect(await screen.findByText(/asked to return to an address it may not use/)).toBeInTheDocument()
    expect(screen.getByText('Close this tab.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Deny' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(assign).not.toHaveBeenCalled()
  })

  it('a redirectable error returns to the application with it', async () => {
    const back = 'https://claude.ai/api/mcp/auth_callback?error=invalid_scope&state=st4te'
    server.use(
      http.get('/api/oauth/authorize', () =>
        HttpResponse.json({ error: 'invalid_scope', error_description: 'The only scope is harbormaster:read.', redirect_to: back }, { status: 400 }),
      ),
    )
    renderApp(`/oauth/authorize?${QUERY}`)
    await waitFor(() => expect(assign).toHaveBeenCalledWith(back))
    expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument()
  })

  it('never navigates to an address that is not http(s)', async () => {
    serveView()
    server.use(http.post('/api/oauth/authorize', () => HttpResponse.json({ redirect_to: 'javascript:alert(1)' })))
    renderApp(`/oauth/authorize?${QUERY}`)
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Approve' }))
    expect(await screen.findByText(/did not return a valid address/)).toBeInTheDocument()
    expect(assign).not.toHaveBeenCalled()
  })

  it('a failed approval shows the server message and stays', async () => {
    serveView()
    server.use(
      http.post('/api/oauth/authorize', () =>
        HttpResponse.json({ error: 'server_error', error_description: 'Something went wrong on our side. Try again.' }, { status: 400 }),
      ),
    )
    renderApp(`/oauth/authorize?${QUERY}`)
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Approve' }))
    expect(await screen.findByText('Something went wrong on our side. Try again.')).toBeInTheDocument()
    expect(assign).not.toHaveBeenCalled()
  })

  it('says so when connecting applications is turned off', async () => {
    server.use(http.get('/api/oauth/authorize', () => HttpResponse.json({ error: { code: 'not_found', message: 'Not found.' } }, { status: 404 })))
    renderApp(`/oauth/authorize?${QUERY}`)
    expect(await screen.findByText(/not turned on/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument()
  })
})
