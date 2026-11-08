import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { renderHook, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { createElement, type ReactNode } from 'react'
import { describe, expect, it } from 'vitest'
import { LOGS_HISTORY_PAYMENTS, POLLER, POLLER_TIERS, SERVICES_METRICS } from '../test/fixtures'
import { server } from '../test/msw'
import { ApiError } from './client'
import {
  allNull,
  isAwsError,
  isThrottled,
  useAlerts,
  useDataProblems,
  useLogHistory,
  useScaling,
  useServiceMetrics,
  useServicesMetrics,
  useTargetHealth,
  useTaskLogs,
} from './observe'

function setup() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const wrapper = ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client }, children)
  return { client, wrapper }
}

describe('observe hooks', () => {
  it('useServiceMetrics requests /api/services/:id/metrics?range=6h', async () => {
    let requested = ''
    server.use(
      http.get('/api/services/svc1/metrics', ({ request }) => {
        requested = new URL(request.url).pathname + new URL(request.url).search
        return HttpResponse.json({ range: '6h' })
      }),
    )
    const { wrapper } = setup()
    const { result } = renderHook(() => useServiceMetrics('svc1', '6h'), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(requested).toBe('/api/services/svc1/metrics?range=6h')
  })

  it('refetchInterval is 60000 and never runs in the background', () => {
    server.use(http.get('/api/services/svc1/metrics', () => HttpResponse.json({})))
    const { client, wrapper } = setup()
    renderHook(() => useServiceMetrics('svc1', '6h'), { wrapper })
    const query = client.getQueryCache().find({ queryKey: ['service', 'svc1', 'metrics', '6h'] })
    expect(query).toBeDefined()
    expect(query?.observers[0].options.refetchInterval).toBe(60000)
    expect(query?.observers[0].options.refetchIntervalInBackground).toBe(false)
  })

  it('useLogHistory encodes container, q and before', async () => {
    let requested = ''
    server.use(
      http.get('/api/services/svc1/logs', ({ request }) => {
        requested = new URL(request.url).search
        return HttpResponse.json({ lines: [] })
      }),
    )
    const { wrapper } = setup()
    const before = '2026-10-31T12:00:00Z'
    const { result } = renderHook(() => useLogHistory('svc1', { container: 'web app', q: 'a&b', before }), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    const params = new URLSearchParams(requested)
    expect(params.get('container')).toBe('web app')
    expect(params.get('q')).toBe('a&b')
    expect(params.get('before')).toBe(before)
    expect(requested).not.toContain('a&b')
  })
})

describe('useServicesMetrics', () => {
  it('builds sorted repeated id params', async () => {
    let requested = ''
    server.use(
      http.get('/api/services/metrics', ({ request }) => {
        requested = new URL(request.url).search
        return HttpResponse.json(SERVICES_METRICS)
      }),
    )
    const { wrapper } = setup()
    const { result } = renderHook(() => useServicesMetrics(['b', 'a', 'b', 'c']), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(requested).toBe('?id=a&id=b&id=c')
  })

  it('is disabled with no ids', () => {
    let called = false
    server.use(
      http.get('/api/services/metrics', () => {
        called = true
        return HttpResponse.json(SERVICES_METRICS)
      }),
    )
    const { wrapper } = setup()
    const { result } = renderHook(() => useServicesMetrics([]), { wrapper })
    expect(result.current.fetchStatus).toBe('idle')
    expect(called).toBe(false)
  })

  it('keeps previous data while refetching for another page', async () => {
    const { wrapper } = setup()
    const { result, rerender } = renderHook(({ ids }) => useServicesMetrics(ids), { wrapper, initialProps: { ids: ['a'] } })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    const first = result.current.data
    server.use(http.get('/api/services/metrics', () => new Promise(() => {})))
    rerender({ ids: ['b'] })
    expect(result.current.data).toBe(first)
    expect(result.current.isPlaceholderData).toBe(true)
  })

  it('polls every 60 s and not in the background', () => {
    const { client, wrapper } = setup()
    renderHook(() => useServicesMetrics(['a']), { wrapper })
    const query = client.getQueryCache().find({ queryKey: ['services', 'metrics', ['a']] })
    expect(query?.observers[0].options.refetchInterval).toBe(60000)
    expect(query?.observers[0].options.refetchIntervalInBackground).toBe(false)
  })
})

describe('snapshot and history polling', () => {
  it.each<[string, () => unknown, unknown[]]>([
    ['useTargetHealth', () => useTargetHealth('svc1'), ['service', 'svc1', 'target-health']],
    ['useScaling', () => useScaling('svc1'), ['service', 'svc1', 'scaling']],
    ['useAlerts', () => useAlerts({}), ['alerts', {}]],
  ])('%s refetches every 5000 ms, not in the background', (_name, hook, queryKey) => {
    server.use(http.get(/\/api\/.*/, () => HttpResponse.json({})))
    const { client, wrapper } = setup()
    renderHook(hook, { wrapper })
    const options = client.getQueryCache().find({ queryKey })?.observers[0].options
    expect(options?.refetchInterval).toBe(5000)
    expect(options?.refetchIntervalInBackground).toBe(false)
  })

  it('useLogHistory never refetches', () => {
    server.use(http.get('/api/services/svc1/logs', () => HttpResponse.json({})))
    const { client, wrapper } = setup()
    renderHook(() => useLogHistory('svc1', {}), { wrapper })
    const options = client.getQueryCache().find({ queryKey: ['service', 'svc1', 'logs', undefined, undefined, undefined] })?.observers[0].options
    expect(options?.refetchInterval).toBeUndefined()
    expect(options?.staleTime).toBe(Infinity)
  })
})

describe('useDataProblems', () => {
  it('names the regions whose Auto scaling tier fails, while core data stays current', async () => {
    server.use(http.get('/api/poller', () => HttpResponse.json(POLLER_TIERS)))
    const { wrapper } = setup()
    const { result } = renderHook(() => useDataProblems(['Auto scaling']), { wrapper })
    await waitFor(() => expect(result.current.total).toBe(1))
    expect(result.current.affected).toHaveLength(1)
    expect(result.current.affected[0].region).toBe('eu-west-1')
    expect(result.current.affected[0].failingTiers.map((tier) => tier.label)).toEqual(['Auto scaling'])
    expect(result.current.current).toBe(1)
  })

  it('ignores failing tiers the page does not depend on', async () => {
    server.use(http.get('/api/poller', () => HttpResponse.json(POLLER_TIERS)))
    const { wrapper } = setup()
    const { result } = renderHook(() => useDataProblems(['Clusters and services']), { wrapper })
    await waitFor(() => expect(result.current.total).toBe(1))
    expect(result.current.affected).toHaveLength(0)
  })

  it('reports every region current when nothing fails', async () => {
    const { wrapper } = setup()
    const { result } = renderHook(() => useDataProblems(), { wrapper })
    await waitFor(() => expect(result.current.total).toBe(POLLER.items.length))
    expect(result.current.current).toBe(POLLER.items.length)
    expect(result.current.affected).toHaveLength(0)
  })
})

describe('useTaskLogs', () => {
  it('requests the task and limit when enabled', async () => {
    let requested = ''
    server.use(
      http.get('/api/services/svc1/logs', ({ request }) => {
        requested = new URL(request.url).search
        return HttpResponse.json(LOGS_HISTORY_PAYMENTS)
      }),
    )
    const { wrapper } = setup()
    const { result } = renderHook(() => useTaskLogs('svc1', 'a1b2c3d4', { enabled: true }), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(new URLSearchParams(requested).get('task')).toBe('a1b2c3d4')
    expect(new URLSearchParams(requested).get('limit')).toBe('20')
    expect(result.current.data?.lines).toHaveLength(3)
  })

  it('does not fetch when disabled', () => {
    let called = false
    server.use(
      http.get('/api/services/svc1/logs', () => {
        called = true
        return HttpResponse.json(LOGS_HISTORY_PAYMENTS)
      }),
    )
    const { wrapper } = setup()
    const { result } = renderHook(() => useTaskLogs('svc1', 'a1b2c3d4', { enabled: false }), { wrapper })
    expect(result.current.fetchStatus).toBe('idle')
    expect(called).toBe(false)
  })
})

describe('observe helpers', () => {
  it('allNull is true only when every point in every series is null', () => {
    expect(allNull([null, null], [null])).toBe(true)
    expect(allNull([null, 0])).toBe(false)
    expect(allNull([], [null])).toBe(true)
  })

  it('isThrottled and isAwsError read the ApiError code', () => {
    expect(isThrottled(new ApiError('throttled', 'slow', 429))).toBe(true)
    expect(isAwsError(new ApiError('aws_error', 'bad', 502))).toBe(true)
    expect(isAwsError(new Error('x'))).toBe(false)
  })
})
