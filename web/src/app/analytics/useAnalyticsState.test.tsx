import { act, renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { describe, expect, it } from 'vitest'
import { AuthProvider } from '../auth/AuthProvider'
import { useAnalyticsState } from './useAnalyticsState'

function setup(route: string) {
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter initialEntries={[route]}>
        <AuthProvider>
          <Routes>
            <Route path="analytics/:tab" element={children} />
          </Routes>
        </AuthProvider>
      </MemoryRouter>
    </QueryClientProvider>
  )
  return renderHook(() => ({ state: useAnalyticsState(), location: useLocation() }), { wrapper })
}

describe('useAnalyticsState', () => {
  it('reads the tab from the path and the range from ?range=', () => {
    const { result } = setup('/analytics/reliability?range=7d')
    expect(result.current.state.tab).toBe('reliability')
    expect(result.current.state.range).toBe('7d')
  })

  it('unknown tab is null', () => {
    const { result } = setup('/analytics/nope')
    expect(result.current.state.tab).toBeNull()
  })

  it('invalid range falls back to 30d', () => {
    expect(setup('/analytics/delivery?range=1y').result.current.state.range).toBe('30d')
    expect(setup('/analytics/delivery').result.current.state.range).toBe('30d')
  })

  it('setRange keeps account, region and tag, and replaces history', () => {
    const { result } = setup('/analytics/delivery?account=111&region=eu-west-1&tag=2&range=7d')
    act(() => result.current.state.setRange('90d'))
    const params = new URLSearchParams(result.current.location.search)
    expect(params.get('range')).toBe('90d')
    expect(params.get('account')).toBe('111')
    expect(params.get('region')).toBe('eu-west-1')
    expect(params.getAll('tag')).toEqual(['2'])
  })

  it('reads the tag from the global filter and setTags writes ?tag=', async () => {
    const { result } = setup('/analytics/delivery?tag=2&tag=3&range=7d')
    expect(result.current.state.tag).toEqual([2, 3])
    // setTags needs the signed-in user to remember the choice, so retry until /api/me has loaded.
    await waitFor(() => {
      act(() => result.current.state.setTags([1]))
      expect(new URLSearchParams(result.current.location.search).getAll('tag')).toEqual(['1'])
    })
    expect(result.current.state.tag).toEqual([1])
    expect(result.current.state.range).toBe('7d')
  })
})
