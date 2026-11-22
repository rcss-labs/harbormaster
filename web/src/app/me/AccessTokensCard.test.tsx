import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { TokenDTO, TokenListDTO, TokenPolicyDTO } from '../../api/tokens'
import { ME_ADMIN, ME_OMER } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

const DAY = 24 * 60 * 60 * 1000

const POLICY: TokenPolicyDTO = {
  max_ttl_days: 90,
  max_tokens: 10,
  sso_linked: false,
  admin: false,
  mcp_url: '',
  oauth: false,
  log_search_daily_bytes: 20 * 1024 ** 3,
  log_search_used_bytes: 5 * 1024 ** 3,
  log_search_max_bytes_per_search: 2 * 1024 ** 3,
}

const token = (over: Partial<TokenDTO>): TokenDTO => ({
  id: 1,
  public_id: 'hmp_abcdefghijkl',
  name: 'laptop',
  kind: 'pat',
  scope: 'read',
  auth_source: 'password',
  created_at: new Date(Date.now() - 2 * DAY).toISOString(),
  expires_at: new Date(Date.now() + 30 * DAY).toISOString(),
  last_used_at: null,
  last_used_ip: '',
  ...over,
})

const list = (items: TokenDTO[], policy: Partial<TokenPolicyDTO> = {}): TokenListDTO => ({ items, grants: [], policy: { ...POLICY, ...policy } })

const serveTokens = (body: () => TokenListDTO) => server.use(http.get('/api/tokens', () => HttpResponse.json(body())))

afterEach(() => vi.restoreAllMocks())

describe('AccessTokensCard', () => {
  it('shows nothing when tokens are off (404)', async () => {
    server.use(
      http.get('/api/me', () => HttpResponse.json(ME_OMER)),
      http.get('/api/tokens', () => HttpResponse.json({ error: { code: 'not_found', message: 'Not found.' } }, { status: 404 })),
    )
    renderApp('/me/access')
    await screen.findByText('Staging') // the page itself has rendered
    await waitFor(() => expect(screen.queryByText('Access tokens')).not.toBeInTheDocument())
    expect(screen.queryByRole('button', { name: 'Create token' })).not.toBeInTheDocument()
  })

  it('lists tokens and revokes one after confirm', async () => {
    let items = [token({ id: 7, name: 'laptop', last_used_at: new Date(Date.now() - 60_000).toISOString(), last_used_ip: '10.1.2.3' }), token({ id: 8, name: 'ci', public_id: 'hmp_zzzzzzzzzzzz' })]
    const deleted: string[] = []
    server.use(
      http.get('/api/me', () => HttpResponse.json(ME_OMER)),
      http.delete('/api/tokens/:id', ({ params }) => {
        deleted.push(String(params.id))
        items = items.filter((t) => String(t.id) !== params.id)
        return new HttpResponse(null, { status: 204 })
      }),
    )
    serveTokens(() => list(items))
    renderApp('/me/access')

    expect(await screen.findByText('laptop')).toBeInTheDocument()
    expect(screen.getByText('ci')).toBeInTheDocument()
    expect(screen.getByText(/from 10\.1\.2\.3/)).toBeInTheDocument()
    expect(screen.getByText('Never used')).toBeInTheDocument()
    expect(screen.getByText(/Log search by tokens today: .*5.* of .*20/)).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Revoke laptop' }))
    expect(deleted).toEqual([]) // nothing happens before the confirm
    fireEvent.click(await screen.findByRole('button', { name: 'Revoke token' }))
    await waitFor(() => expect(deleted).toEqual(['7']))
    await waitFor(() => expect(screen.queryByText('laptop')).not.toBeInTheDocument())
    expect(screen.getByText('ci')).toBeInTheDocument()
  })

  it('cancelling the confirm revokes nothing', async () => {
    const deleted: string[] = []
    server.use(
      http.get('/api/me', () => HttpResponse.json(ME_OMER)),
      http.delete('/api/tokens/:id', ({ params }) => {
        deleted.push(String(params.id))
        return new HttpResponse(null, { status: 204 })
      }),
    )
    serveTokens(() => list([token({ id: 7 })]))
    renderApp('/me/access')
    fireEvent.click(await screen.findByRole('button', { name: 'Revoke laptop' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }))
    expect(deleted).toEqual([])
    expect(screen.getByText('laptop')).toBeInTheDocument()
  })

  it('shows the token id exactly as the API sends it, without a second prefix', async () => {
    server.use(http.get('/api/me', () => HttpResponse.json(ME_OMER)))
    serveTokens(() => list([token({ public_id: 'hmp_abcdefghijkl' })]))
    renderApp('/me/access')
    expect(await screen.findByText('hmp_abcdefghijkl')).toBeInTheDocument()
    expect(screen.queryByText(/hmp_hmp_/)).not.toBeInTheDocument()
  })

  it('flags a token that expires inside three days', async () => {
    server.use(http.get('/api/me', () => HttpResponse.json(ME_OMER)))
    serveTokens(() =>
      list([
        token({ id: 1, name: 'soon', expires_at: new Date(Date.now() + 1.5 * DAY).toISOString() }),
        token({ id: 2, name: 'later', public_id: 'hmp_llllllllllll', expires_at: new Date(Date.now() + 20 * DAY).toISOString() }),
      ]),
    )
    renderApp('/me/access')
    const soonRow = (await screen.findByText('soon')).closest('div')!.parentElement as HTMLElement
    const laterRow = screen.getByText('later').closest('div')!.parentElement as HTMLElement
    const soon = within(soonRow).getByText(/^Expires in 2 days$/)
    const later = within(laterRow).getByText(/^Expires in 20 days$/)
    expect(soon.className).not.toBe(later.className)
  })

  it('create shows the secret once and the claude mcp add line', async () => {
    const secret = 'hmp_abcdefghijkl_' + 'S'.repeat(43)
    const bodies: unknown[] = []
    server.use(
      http.get('/api/me', () => HttpResponse.json(ME_OMER)),
      http.post('/api/tokens', async ({ request }) => {
        bodies.push(await request.json())
        return HttpResponse.json({ token: token({ id: 9, name: 'my laptop' }), secret }, { status: 201 })
      }),
    )
    serveTokens(() => list([], { mcp_url: 'https://mcp.example.com/' }))
    renderApp('/me/access')

    fireEvent.click(await screen.findByRole('button', { name: 'Create token' }))
    await screen.findByPlaceholderText('Claude Code on my laptop')
    const dialogCreate = () => {
      const all = screen.getAllByRole('button', { name: 'Create token' })
      return all[all.length - 1]
    }
    expect(dialogCreate()).toBeDisabled() // a name is required
    fireEvent.change(screen.getByPlaceholderText('Claude Code on my laptop'), { target: { value: ' my laptop ' } })
    fireEvent.change(screen.getByRole('combobox'), { target: { value: '30' } })
    fireEvent.click(dialogCreate())

    expect(await screen.findByTestId('token-secret')).toHaveTextContent(secret)
    expect(bodies).toEqual([{ name: 'my laptop', expires_in_days: 30 }])
    expect(screen.getByText('This is the only time the token is shown.')).toBeInTheDocument()
    expect(
      screen.getByText(`claude mcp add --transport http harbormaster https://mcp.example.com/mcp --header "Authorization: Bearer ${secret}"`),
    ).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Done' }))
    await waitFor(() => expect(screen.queryByTestId('token-secret')).not.toBeInTheDocument())
    expect(document.body.textContent).not.toContain(secret)

    // Reopening starts from an empty form: the secret is gone.
    fireEvent.click(screen.getByRole('button', { name: 'Create token' }))
    expect(await screen.findByPlaceholderText('Claude Code on my laptop')).toHaveValue('')
    expect(screen.queryByTestId('token-secret')).not.toBeInTheDocument()
    expect(document.body.textContent).not.toContain(secret)
  })

  it('omits the connect command when no gateway URL is set', async () => {
    const secret = 'hmp_abcdefghijkl_' + 'S'.repeat(43)
    server.use(
      http.get('/api/me', () => HttpResponse.json(ME_OMER)),
      http.post('/api/tokens', () => HttpResponse.json({ token: token({ id: 9 }), secret }, { status: 201 })),
    )
    serveTokens(() => list([]))
    renderApp('/me/access')
    fireEvent.click(await screen.findByRole('button', { name: 'Create token' }))
    fireEvent.change(await screen.findByPlaceholderText('Claude Code on my laptop'), { target: { value: 'x' } })
    const buttons = screen.getAllByRole('button', { name: 'Create token' })
    fireEvent.click(buttons[buttons.length - 1])
    await screen.findByTestId('token-secret')
    expect(screen.queryByText(/claude mcp add/)).not.toBeInTheDocument()
  })

  it('caps the lifetime choices at the policy maximum', async () => {
    const choices = async () => {
      fireEvent.click(await screen.findByRole('button', { name: 'Create token' }))
      const select = await screen.findByRole('combobox')
      return { select, days: within(select).getAllByRole('option').map((o) => Number((o as HTMLOptionElement).value)) }
    }

    // A password user who is not an admin: up to 90 days, no explanation.
    server.use(http.get('/api/me', () => HttpResponse.json(ME_OMER)))
    serveTokens(() => list([]))
    const plain = renderApp('/me/access')
    let result = await choices()
    expect(Math.max(...result.days)).toBe(90)
    expect((result.select as HTMLSelectElement).value).toBe('90')
    expect(screen.queryByText(/at most 7 days/)).not.toBeInTheDocument()
    plain.unmount()

    // SSO-linked: 7 days, with its own line.
    serveTokens(() => list([], { max_ttl_days: 7, sso_linked: true }))
    const sso = renderApp('/me/access')
    result = await choices()
    expect(Math.max(...result.days)).toBe(7)
    expect(screen.getByText(/single sign-on users last at most 7 days/)).toBeInTheDocument()
    sso.unmount()

    // Admin: 7 days, a different line.
    server.use(http.get('/api/me', () => HttpResponse.json(ME_ADMIN)))
    serveTokens(() => list([], { max_ttl_days: 7, admin: true }))
    renderApp('/me/access')
    result = await choices()
    expect(Math.max(...result.days)).toBe(7)
    expect(screen.getByText(/global admins last at most 7 days/)).toBeInTheDocument()
    expect(screen.queryByText(/single sign-on/)).not.toBeInTheDocument()
  })

  it('disables Create at the token limit', async () => {
    server.use(http.get('/api/me', () => HttpResponse.json(ME_OMER)))
    serveTokens(() => list([token({ id: 1 }), token({ id: 2, public_id: 'hmp_bbbbbbbbbbbb' })], { max_tokens: 2 }))
    renderApp('/me/access')
    expect(await screen.findByRole('button', { name: 'Create token' })).toBeDisabled()
  })
})
