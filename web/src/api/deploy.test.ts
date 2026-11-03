import { renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createElement, type ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from './client'
import { useChangeSet, useDeploySettings, useRevisionDefinition } from './deploy'

function renderWithClient<T>(useHook: () => T) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  const invalidate = vi.spyOn(queryClient, 'invalidateQueries')
  const wrapper = ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client: queryClient }, children)
  return { ...renderHook(useHook, { wrapper }), invalidate }
}

afterEach(() => vi.restoreAllMocks())

describe('deploy hooks', () => {
  it('useDeploySettings reads the deploy-settings endpoint', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue({ version: 'v' })
    const { result } = renderWithClient(() => useDeploySettings('svc-1'))
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(get).toHaveBeenCalledWith('/api/services/svc-1/deploy-settings')
  })

  it('useRevisionDefinition stays idle for null and reads the revision otherwise', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue({ revision: 41 })
    const idle = renderWithClient(() => useRevisionDefinition('svc-1', null))
    expect(idle.result.current.fetchStatus).toBe('idle')
    const live = renderWithClient(() => useRevisionDefinition('svc-1', 41))
    await waitFor(() => expect(live.result.current.isSuccess).toBe(true))
    expect(get).toHaveBeenCalledWith('/api/services/svc-1/revisions/41')
  })

  it('useChangeSet posts the body and refetches service, operations, deploy-settings and scaling', async () => {
    const post = vi.spyOn(api, 'post').mockResolvedValue({ status: 'applied' })
    const { result, invalidate } = renderWithClient(() => useChangeSet('svc-1'))
    const body = { base_revision: 42, base_version: 'v', reason: 'why', service: { desired_count: 4 } }
    await result.current.mutateAsync(body)

    expect(post).toHaveBeenCalledWith('/api/services/svc-1/changeset', body)
    const keys = invalidate.mock.calls.map(([filters]) => (filters as { queryKey: unknown[] }).queryKey)
    expect(keys).toContainEqual(['service', 'svc-1'])
    expect(keys).toContainEqual(['operations', 'svc-1'])
    expect(keys).toContainEqual(['service', 'svc-1', 'deploy-settings'])
    expect(keys).toContainEqual(['service', 'svc-1', 'scaling'])
  })
})
