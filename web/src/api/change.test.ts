import { renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createElement, type ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from './client'
import { isApiError, useDeploy, useRegisterRevision, useSetDesiredCount, useStopTask } from './change'

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
    const post = vi.spyOn(api, 'post').mockResolvedValue({ task_arn: 'arn:task', reason: 'Stopped by sam via Harbormaster' })
    const { result, invalidate } = renderWithClient(() => useStopTask('svc-1'))

    await result.current.mutateAsync('task-9')

    expect(post).toHaveBeenCalledWith('/api/tasks/task-9/stop')
    await waitFor(() => expect(invalidate).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ['tasks'] })))
    expect(invalidate).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ['service', 'svc-1', 'tasks'] }))
    expect(invalidate).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ['service', 'svc-1'] }))
  })
})

// The server's snapshot only catches up with AWS on the next poll (about 5 s), and the page itself refetches every 5 s.
// Two timers of the same period drift in and out of step, so a single refetch right after the write can miss the
// poll by a few hundred milliseconds and leave the old value on screen for a whole extra period.
describe('refetching while a write settles', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  const countServiceRefetches = (invalidate: ReturnType<typeof renderWithClient>['invalidate']) =>
    invalidate.mock.calls.filter(([filters]) => JSON.stringify(filters?.queryKey) === JSON.stringify(['service', 'svc-1'])).length

  it('useSetDesiredCount refetches the service every 2 s for 10 s after the write, then stops', async () => {
    vi.useFakeTimers()
    vi.spyOn(api, 'post').mockResolvedValue({ desired: 3, previous: 2, changed: true })
    const { result, invalidate } = renderWithClient(() => useSetDesiredCount('svc-1'))

    await result.current.mutateAsync({ desired: 3 })
    const immediate = countServiceRefetches(invalidate)
    expect(immediate).toBeGreaterThan(0)

    await vi.advanceTimersByTimeAsync(2_000)
    expect(countServiceRefetches(invalidate)).toBe(immediate + 1)
    await vi.advanceTimersByTimeAsync(8_000)
    expect(countServiceRefetches(invalidate)).toBe(immediate + 5)
    await vi.advanceTimersByTimeAsync(10_000)
    expect(countServiceRefetches(invalidate)).toBe(immediate + 5)
  })

  it('useStopTask refetches the task lists the same way', async () => {
    vi.useFakeTimers()
    vi.spyOn(api, 'post').mockResolvedValue({ task_arn: 'arn:task', reason: 'Stopped by sam via Harbormaster' })
    const { result, invalidate } = renderWithClient(() => useStopTask('svc-1'))

    await result.current.mutateAsync('task-9')
    const countTaskRefetches = () => invalidate.mock.calls.filter(([filters]) => JSON.stringify(filters?.queryKey) === JSON.stringify(['tasks'])).length
    const immediate = countTaskRefetches()

    await vi.advanceTimersByTimeAsync(10_000)
    expect(countTaskRefetches()).toBe(immediate + 5)
    await vi.advanceTimersByTimeAsync(10_000)
    expect(countTaskRefetches()).toBe(immediate + 5)
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
