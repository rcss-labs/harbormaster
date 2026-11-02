import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { renderHook, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { createElement, type ReactNode } from 'react'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import { AuthProvider } from '../app/auth/AuthProvider'
import { server } from '../test/msw'
import { useAnalytics } from './analytics'

function setup(route: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(
      QueryClientProvider,
      { client },
      createElement(MemoryRouter, { initialEntries: [route] }, createElement(AuthProvider, null, children)),
    )
  return { client, wrapper }
}

describe('useAnalytics', () => {
  it('sends range and scope params', async () => {
    let requested = ''
    server.use(
      http.get('/api/analytics/delivery', ({ request }) => {
        const url = new URL(request.url)
        requested = url.pathname + url.search
        return HttpResponse.json({ tab: 'delivery' })
      }),
    )
    const { wrapper } = setup('/analytics/delivery?account=111&region=eu-west-1&tag=2&tag=3')
    const { result } = renderHook(() => useAnalytics('delivery', '7d'), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    const params = new URL(requested, 'http://x').searchParams
    expect(params.get('range')).toBe('7d')
    expect(params.get('account')).toBe('111')
    expect(params.get('region')).toBe('eu-west-1')
    expect(params.getAll('tag')).toEqual(['2', '3'])
  })

  it('query key includes tab, range and filters', async () => {
    const { client, wrapper } = setup('/analytics/capacity?account=111&tag=2')
    const { result } = renderHook(() => useAnalytics('capacity', '90d'), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(client.getQueryCache().find({ queryKey: ['analytics', 'capacity', '90d', { account: '111', region: undefined, tag: [2] }] })).toBeDefined()
  })

  it('polls once a minute but not in the background', async () => {
    const { client, wrapper } = setup('/analytics/delivery')
    const { result } = renderHook(() => useAnalytics('delivery', '30d'), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    const observer = client.getQueryCache().findAll({ queryKey: ['analytics'] })[0].observers[0]
    expect(observer.options.refetchInterval).toBe(60000)
    expect(observer.options.refetchIntervalInBackground).toBe(false)
  })

  it('keeps the previous data while a new range loads', async () => {
    const { wrapper } = setup('/analytics/delivery')
    const { result, rerender } = renderHook(({ range }) => useAnalytics('delivery', range), {
      wrapper,
      initialProps: { range: '30d' as const } as { range: '24h' | '30d' },
    })
    await waitFor(() => expect(result.current.data?.window.requested).toBe('30d'))
    server.use(http.get('/api/analytics/delivery', async () => new Promise<never>(() => undefined)))
    rerender({ range: '24h' })
    expect(result.current.isPlaceholderData).toBe(true)
    expect(result.current.data?.window.requested).toBe('30d')
  })
})
