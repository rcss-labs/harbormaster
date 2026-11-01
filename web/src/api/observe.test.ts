import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { renderHook, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { createElement, type ReactNode } from 'react'
import { describe, expect, it } from 'vitest'
import { server } from '../test/msw'
import { ApiError } from './client'
import { allNull, isAwsError, isThrottled, useAlerts, useLogHistory, useScaling, useServiceMetrics, useTargetHealth } from './observe'

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
