import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import type { GrantDTO, TokenListDTO } from '../../api/tokens'
import { ME_OMER } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

const IN_DAYS = (days: number) => new Date(Date.now() + days * 86_400_000).toISOString()

const GRANT: GrantDTO = {
  id: 7,
  client_id: 'https://claude.ai/oauth/mcp-client.json',
  client_name: 'Claude',
  scope: 'harbormaster:read',
  created_at: '2026-11-20T10:00:00Z',
  expires_at: IN_DAYS(6),
  last_used_at: null,
}

function tokenList(overrides: Partial<TokenListDTO['policy']>, grants: GrantDTO[]): TokenListDTO {
  return {
    items: [],
    grants,
    policy: {
      max_ttl_days: 90,
      max_tokens: 10,
      sso_linked: false,
      admin: false,
      mcp_url: 'https://mcp.example.com',
      oauth: true,
      log_search_daily_bytes: 0,
      log_search_used_bytes: 0,
      log_search_max_bytes_per_search: 0,
      ...overrides,
    },
  }
}

describe('ConnectedAppsCard', () => {
  it('lists connected apps and revokes one', async () => {
    let grants = [GRANT, { ...GRANT, id: 8, client_name: 'Claude Code', last_used_at: '2026-11-22T09:00:00Z' }]
    const revoked: string[] = []
    server.use(
      http.get('/api/me', () => HttpResponse.json(ME_OMER)),
      http.get('/api/tokens', () => HttpResponse.json(tokenList({}, grants))),
      http.delete('/api/oauth/grants/:id', ({ params }) => {
        revoked.push(String(params.id))
        grants = grants.filter((g) => String(g.id) !== params.id)
        return new HttpResponse(null, { status: 204 })
      }),
    )
    renderApp('/me/access')
    const panel = (await screen.findByText('Connected apps')).closest('section') ?? document.body
    expect(await within(panel).findByText('Claude')).toBeInTheDocument()
    expect(within(panel).getByText('Claude Code')).toBeInTheDocument()
    expect(within(panel).getByText('Never used')).toBeInTheDocument()
    expect(within(panel).getByText(/Last used/)).toBeInTheDocument()
    expect(within(panel).getAllByText(GRANT.client_id)).toHaveLength(2)

    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Revoke Claude' }))
    // Nothing is revoked until the dialog is confirmed.
    expect(revoked).toEqual([])
    await user.click(await screen.findByRole('button', { name: 'Disconnect' }))
    await waitFor(() => expect(revoked).toEqual(['7']))
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Revoke Claude' })).not.toBeInTheDocument())
    expect(screen.getByRole('button', { name: 'Revoke Claude Code' })).toBeInTheDocument()
  })

  it('cancelling the dialog revokes nothing', async () => {
    let called = false
    server.use(
      http.get('/api/me', () => HttpResponse.json(ME_OMER)),
      http.get('/api/tokens', () => HttpResponse.json(tokenList({}, [GRANT]))),
      http.delete('/api/oauth/grants/:id', () => {
        called = true
        return new HttpResponse(null, { status: 204 })
      }),
    )
    renderApp('/me/access')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Revoke Claude' }))
    await user.click(await screen.findByRole('button', { name: /cancel/i }))
    expect(called).toBe(false)
    expect(screen.getByRole('button', { name: 'Revoke Claude' })).toBeInTheDocument()
  })

  it('shows an empty state when nothing is connected', async () => {
    server.use(
      http.get('/api/me', () => HttpResponse.json(ME_OMER)),
      http.get('/api/tokens', () => HttpResponse.json(tokenList({}, []))),
    )
    renderApp('/me/access')
    expect(await screen.findByText(/No connected apps/)).toBeInTheDocument()
  })

  it('is hidden while OAuth is off', async () => {
    server.use(
      http.get('/api/me', () => HttpResponse.json(ME_OMER)),
      http.get('/api/tokens', () => HttpResponse.json(tokenList({ oauth: false }, []))),
    )
    renderApp('/me/access')
    await screen.findByText('Access tokens')
    expect(screen.queryByText('Connected apps')).not.toBeInTheDocument()
  })

  it('tells how to connect claude.ai only when OAuth is on and a gateway is set', async () => {
    server.use(
      http.get('/api/me', () => HttpResponse.json(ME_OMER)),
      http.get('/api/tokens', () => HttpResponse.json(tokenList({}, []))),
    )
    renderApp('/me/access')
    expect(await screen.findByText(/Add custom connector/)).toBeInTheDocument()
    expect(screen.getByText(/https:\/\/mcp\.example\.com\/mcp/)).toBeInTheDocument()
  })

  it('does not tell how to connect claude.ai when OAuth is off', async () => {
    server.use(
      http.get('/api/me', () => HttpResponse.json(ME_OMER)),
      http.get('/api/tokens', () => HttpResponse.json(tokenList({ oauth: false }, []))),
    )
    renderApp('/me/access')
    await screen.findByText('Access tokens')
    expect(screen.queryByText(/Add custom connector/)).not.toBeInTheDocument()
  })
})
