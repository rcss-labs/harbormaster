import { renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createElement, type ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from './client'
import { isApiError, useDeploy, useRegisterRevision, useStopTask } from './change'

function renderWithClient<T>(useHook: () => T) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  const invalidate = vi.spyOn(queryClient, 'invalidateQueries')
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client: queryClient }, children)
  const hook = renderHook(useHook, { wrapper })
  return { ...hook, invalidate }
}

afterEach(() => {
  vi.restoreAllMocks()
})

describe('useRegisterRevision', () => {
  it('posts base_revision and definition and invalidates the service query', async () => {
    const post = vi.spyOn(api, 'post').mockResolvedValue({ revision: 43 })
    const definition = { family: 'payments-api', containerDefinitions: [] }
    const { result, invalidate } = renderWithClient(() => useRegisterRevision('svc-1'))

    await result.current.mutateAsync({ base_revision: 42, definition })

    expect(post).toHaveBeenCalledWith('/api/services/svc-1/revisions', { base_revision: 42, definition })
    expect(invalidate).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ['service', 'svc-1'] }))
  })
})

describe('useDeploy', () => {
  it('posts the revision and invalidates the service and its operations', async () => {
    const post = vi.spyOn(api, 'post').mockResolvedValue({ deployment_id: 'd-1', from_revision: 42, to_revision: 43, kind: 'deploy' })
    const { result, invalidate } = renderWithClient(() => useDeploy('svc-1'))

    await result.current.mutateAsync({ revision: 43 })

    expect(post).toHaveBeenCalledWith('/api/services/svc-1/deploy', { revision: 43 })
    expect(invalidate).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ['service', 'svc-1'] }))
    expect(invalidate).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ['operations', 'svc-1'] }))
  })
})

describe('useStopTask', () => {
  it('posts to /api/tasks/{id}/stop and invalidates the task lists', async () => {
    const post = vi.spyOn(api, 'post').mockResolvedValue({ task_arn: 'arn:task', reason: 'Stopped by pavel via Harbormaster' })
    const { result, invalidate } = renderWithClient(() => useStopTask('svc-1'))

    await result.current.mutateAsync('task-9')

    expect(post).toHaveBeenCalledWith('/api/tasks/task-9/stop')
    await waitFor(() => expect(invalidate).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ['tasks'] })))
    expect(invalidate).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ['service', 'svc-1', 'tasks'] }))
    expect(invalidate).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ['service', 'svc-1'] }))
  })
})

describe('isApiError', () => {
  it('narrows on code', () => {
    const conflict = new ApiError('conflict', 'stale', 409)
    expect(isApiError(conflict)).toBe(true)
    expect(isApiError(conflict, 'conflict')).toBe(true)
    expect(isApiError(conflict, 'invalid')).toBe(false)
    expect(isApiError(new Error('boom'))).toBe(false)
    expect(isApiError(undefined)).toBe(false)
  })
})
