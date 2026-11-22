import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import type { TokenDTO } from '../../../api/tokens'
import type { UserDTO } from '../../../api/types'
import { ToastProvider } from '../../../components/Toast'
import { USERS } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { UserTokensDialog } from './UserTokensDialog'

const omer = USERS.items.find((user) => user.username === 'omer') as UserDTO

const token = (over: Partial<TokenDTO>): TokenDTO => ({
  id: 1,
  public_id: 'hmp_abcdefghijkl',
  name: 'laptop',
  kind: 'pat',
  scope: 'read',
  auth_source: 'password',
  created_at: new Date().toISOString(),
  expires_at: new Date(Date.now() + 30 * 24 * 60 * 60 * 1000).toISOString(),
  last_used_at: null,
  last_used_ip: '',
  ...over,
})

function renderDialog(onClose = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <ToastProvider>
        <UserTokensDialog user={omer} onClose={onClose} />
      </ToastProvider>
    </QueryClientProvider>,
  )
}

describe('UserTokensDialog', () => {
  it('admin dialog lists and revokes a user\'s token', async () => {
    let items = [token({ id: 5, name: 'laptop' }), token({ id: 6, name: 'ci', public_id: 'hmp_zzzzzzzzzzzz' })]
    const deleted: string[] = []
    server.use(
      http.get(`/api/users/${omer.id}/tokens`, () => HttpResponse.json({ items })),
      http.delete(`/api/users/${omer.id}/tokens/:tokenId`, ({ params }) => {
        deleted.push(String(params.tokenId))
        items = items.filter((t) => String(t.id) !== params.tokenId)
        return new HttpResponse(null, { status: 204 })
      }),
    )
    renderDialog()
    expect(await screen.findByText('laptop')).toBeInTheDocument()
    expect(screen.getByText('ci')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Revoke laptop' }))
    expect(deleted).toEqual([])
    fireEvent.click(await screen.findByRole('button', { name: 'Revoke token' }))
    await waitFor(() => expect(deleted).toEqual(['5']))
    await waitFor(() => expect(screen.queryByText('laptop')).not.toBeInTheDocument())
    expect(await screen.findByText('ci')).toBeInTheDocument()
  })

  it('shows the token id exactly as the API sends it, without a second prefix', async () => {
    server.use(http.get(`/api/users/${omer.id}/tokens`, () => HttpResponse.json({ items: [token({ public_id: 'hmp_abcdefghijkl' })] })))
    renderDialog()
    expect(await screen.findByText('hmp_abcdefghijkl')).toBeInTheDocument()
    expect(screen.queryByText(/hmp_hmp_/)).not.toBeInTheDocument()
  })

  it('says when the user has no tokens', async () => {
    server.use(http.get(`/api/users/${omer.id}/tokens`, () => HttpResponse.json({ items: [] })))
    renderDialog()
    expect(await screen.findByText('omer has no tokens.')).toBeInTheDocument()
  })

  it('shows an error in place when the list fails', async () => {
    server.use(http.get(`/api/users/${omer.id}/tokens`, () => HttpResponse.json({ error: { code: 'internal', message: 'Boom.' } }, { status: 500 })))
    renderDialog()
    expect(await screen.findByRole('alert')).toHaveTextContent('Boom.')
  })
})
