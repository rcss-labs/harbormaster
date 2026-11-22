import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, renderHook } from '@testing-library/react'
import { setTimeout as realSleep } from 'node:timers/promises'
import { http, HttpResponse } from 'msw'
import { createElement, type ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { FOCUS_CAPPED, FOCUS_LIVE } from '../test/focusFixtures'
import { SERVICE_PAYMENTS } from '../test/fixtures'
import { server } from '../test/msw'
import { useFocus, type FocusDTO } from './focus'
import { useService } from './hooks'

let pageFetches = 0
let focusReply: FocusDTO = FOCUS_LIVE

beforeEach(() => {
  pageFetches = 0
  focusReply = FOCUS_LIVE
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' })
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] })
  server.use(
    http.get('/api/services/:id', () => {
      pageFetches++
      return HttpResponse.json(SERVICE_PAYMENTS)
    }),
    http.post('/api/focus', () => HttpResponse.json(focusReply)),
    http.delete('/api/focus', () => new HttpResponse(null, { status: 204 })),
  )
})

afterEach(async () => {
  cleanup()
  await realSleep(20)
  vi.useRealTimers()
  Reflect.deleteProperty(document, 'visibilityState')
})

const tick = async (ms: number) => {
  // In 1 s steps, so each request settles before the next timer fires (the network is real).
  for (let elapsed = 0; elapsed < ms; elapsed += 1000) {
    await vi.advanceTimersByTimeAsync(Math.min(1000, ms - elapsed))
    await realSleep(15)
  }
  await realSleep(15)
}

const wrapper = ({ children }: { children: ReactNode }) =>
  createElement(QueryClientProvider, { client: new QueryClient({ defaultOptions: { queries: { retry: false } } }) }, children)

/** Mirrors ServicePage: the page query and the focus heartbeat on the same service. */
function renderPage() {
  return renderHook(
    () => {
      const query = useService('svc-1')
      return useFocus('service', 'svc-1', query.refetch)
    },
    { wrapper },
  )
}

describe('a focused page polls once', () => {
  it('does not also run the 5 s interval while focus is live', async () => {
    renderPage()
    await tick(16_000)
    // 1 initial read + 3 fast-follow refetches; the focus reply never changes, so no heartbeat refresh. The 5 s interval would add 2 more.
    expect(pageFetches).toBe(4)
  })

  it('falls back to the 5 s interval when focus is not live', async () => {
    focusReply = FOCUS_CAPPED
    renderPage()
    await tick(16_000)
    // initial + 3 fast-follow + interval ticks (the interval restarts after each fetch: 8 s and 13 s)
    expect(pageFetches).toBeGreaterThanOrEqual(6)
  })

  it('refreshes the page when a heartbeat reports a newer live read', async () => {
    let reads = 0
    server.use(
      http.post('/api/focus', () => HttpResponse.json({ ...FOCUS_LIVE, refreshed_at: `2026-01-01T00:00:${String(10 + reads++).padStart(2, '0')}Z` })),
    )
    renderPage()
    await tick(5000)
    const before = pageFetches
    await tick(10_000)
    expect(pageFetches).toBeGreaterThan(before)
  })

  it('does not re-render when a heartbeat returns an equal focus DTO', async () => {
    let renders = 0
    renderHook(
      () => {
        renders++
        return useFocus('service', 'svc-1', vi.fn().mockResolvedValue(undefined))
      },
      { wrapper },
    )
    await tick(4000) // the open and its recheck have both answered
    await act(async () => {})
    const settled = renders
    await tick(30_000)
    expect(renders).toBe(settled)
  })
})
