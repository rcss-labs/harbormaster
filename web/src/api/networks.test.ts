import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { renderHook, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { createElement, type ReactNode } from 'react'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import { AuthProvider } from '../app/auth/AuthProvider'
import { server } from '../test/msw'
import { useNetworks, useVpcTree } from './networks'
import { useStorage } from './storage'

// Filters come from the URL (?account=&region=), so the hooks need the router and auth providers but no signed-in user.
function setup(route = '/') {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(
      QueryClientProvider,
      { client },
      createElement(MemoryRouter, { initialEntries: [route] }, createElement(AuthProvider, null, children)),
    )
  return { client, wrapper }
}

describe('networks and storage hooks', () => {
  it('useVpcTree builds the tree URL with account and region', async () => {
    let requested = ''
    server.use(
      http.get('/api/vpcs/vpc-1/tree', ({ request }) => {
        requested = new URL(request.url).pathname + new URL(request.url).search
        return HttpResponse.json({})
      }),
    )
    const { wrapper } = setup()
    const { result } = renderHook(() => useVpcTree('111111111111', 'eu-west-1', 'vpc-1'), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(requested).toBe('/api/vpcs/vpc-1/tree?account=111111111111&region=eu-west-1')
  })

  it('useStorage refetches every 60000 ms and never in the background', async () => {
    server.use(http.get('/api/storage', () => HttpResponse.json({})))
    const { client, wrapper } = setup('/?account=111111111111&region=eu-west-1')
    const { result } = renderHook(() => useStorage(), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    const options = client.getQueryCache().find({ queryKey: ['storage', { account: '111111111111', region: 'eu-west-1' }] })?.observers[0].options
    expect(options?.refetchInterval).toBe(60000)
    expect(options?.refetchIntervalInBackground).toBe(false)
  })

  it('useNetworks sends the URL account and region filters and refetches every 5000 ms', async () => {
    let requested = ''
    server.use(
      http.get('/api/networks', ({ request }) => {
        requested = new URL(request.url).search
        return HttpResponse.json({ items: [], polled_at: '2026-10-31T12:00:00Z' })
      }),
    )
    const { client, wrapper } = setup('/?account=111111111111')
    const { result } = renderHook(() => useNetworks(), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(requested).toBe('?account=111111111111')
    const options = client.getQueryCache().find({ queryKey: ['networks', { account: '111111111111', region: undefined }] })?.observers[0].options
    expect(options?.refetchInterval).toBe(5000)
  })
})
