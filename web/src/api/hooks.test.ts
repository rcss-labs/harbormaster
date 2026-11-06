import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { renderHook, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { createElement, type ReactNode } from 'react'
import { describe, expect, it } from 'vitest'
import { SERVICES_LIST } from '../test/fixtures'
import { server } from '../test/msw'
import { queryString, useServicesList } from './hooks'

describe('queryString', () => {
  it('repeats the key once per array element', () => {
    expect(queryString({ tag: [2, 3], account: 'a' })).toBe('?account=a&tag=2&tag=3')
  })

  it('omits empty arrays', () => {
    expect(queryString({ tag: [] })).toBe('')
  })

  it('omits undefined and empty values', () => {
    expect(queryString({ tag: undefined, q: '' })).toBe('')
  })
})

describe('useServicesList', () => {
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client: new QueryClient({ defaultOptions: { queries: { retry: false } } }) }, children)

  it('sends account, region and every tag as query parameters', async () => {
    let requested = ''
    server.use(
      http.get('/api/services', ({ request }) => {
        requested = new URL(request.url).search
        return HttpResponse.json(SERVICES_LIST)
      }),
    )
    const { result } = renderHook(() => useServicesList({ account: '482109375512', region: 'us-east-1', tag: [2, 3] }), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(requested).toBe('?account=482109375512&region=us-east-1&tag=2&tag=3')
  })

  it('returns all fifteen services unfiltered', async () => {
    const { result } = renderHook(() => useServicesList(), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(result.current.data?.items).toHaveLength(15)
    expect(result.current.data?.truncated).toBe(false)
  })
})
