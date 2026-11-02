import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { renderHook, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import type { ReactNode } from 'react'
import { describe, expect, it } from 'vitest'
import { server } from '../../test/msw'
import { useAllTasks } from './useAllTasks'

describe('useAllTasks', () => {
  it('returns the same items array across re-renders while the data is unchanged', async () => {
    server.use(
      http.get('/api/tasks', () =>
        HttpResponse.json({ items: [{ task_arn: 'a' }, { task_arn: 'b' }], next: null, summary: {} }),
      ),
    )
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    const filters = {}
    const { result, rerender } = renderHook(() => useAllTasks(filters), { wrapper })
    await waitFor(() => expect(result.current.items).toHaveLength(2))
    const first = result.current.items
    rerender()
    expect(result.current.items).toBe(first)
  })
})
