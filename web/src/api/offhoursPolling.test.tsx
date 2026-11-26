import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, renderHook } from '@testing-library/react'
import { setTimeout as realSleep } from 'node:timers/promises'
import { http, HttpResponse } from 'msw'
import { createElement, type ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { OFFHOURS_ASLEEP, OFFHOURS_PRODUCTION, OFFHOURS_STAGING_DETAIL } from '../test/offhoursFixtures'
import { server } from '../test/msw'
import { useOffHoursSchedule } from './offhours'
import { LIVE_REFETCH_MS } from './polling'

let fetches = 0
let rows = [OFFHOURS_PRODUCTION]
let gone = false
let visibility: 'visible' | 'hidden' = 'visible'

beforeEach(() => {
  fetches = 0
  rows = [OFFHOURS_PRODUCTION]
  gone = false
  visibility = 'visible'
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => visibility })
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] })
  server.use(
    http.get('/api/offhours/schedules/1', () => {
      fetches++
      if (gone) return HttpResponse.json({ error: { code: 'not_found', message: 'Not found.' } }, { status: 404 })
      return HttpResponse.json({ ...OFFHOURS_STAGING_DETAIL, services: rows })
    }),
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

describe('useOffHoursSchedule polling', () => {
  it('refreshes while a row is pending and shows the row as asleep once the reconciler has caught up', async () => {
    const { result } = renderHook(() => useOffHoursSchedule(1), { wrapper })
    await tick(100)
    expect(result.current.data?.services[0].state).toBe('pending')

    rows = [{ ...OFFHOURS_ASLEEP, service_id: OFFHOURS_PRODUCTION.service_id }]
    await tick(LIVE_REFETCH_MS)
    expect(result.current.data?.services[0].state).toBe('asleep')

    // Settled: the page goes quiet.
    const settledAt = fetches
    await tick(LIVE_REFETCH_MS * 4)
    expect(fetches).toBe(settledAt)
  })

  it('does not poll for a row that stays pending because it needs attention', async () => {
    rows = [{ ...OFFHOURS_PRODUCTION, reason: 'Needs attention: asleep with no wake action. Check its scheduled actions.' }]
    renderHook(() => useOffHoursSchedule(1), { wrapper })
    await tick(100)
    expect(fetches).toBe(1)
    await tick(LIVE_REFETCH_MS * 4)
    expect(fetches).toBe(1)
  })

  it('stops polling when the schedule has been deleted elsewhere', async () => {
    renderHook(() => useOffHoursSchedule(1), { wrapper })
    await tick(100)
    gone = true
    await tick(LIVE_REFETCH_MS)
    const afterNotFound = fetches
    expect(afterNotFound).toBe(2)
    await tick(LIVE_REFETCH_MS * 4)
    expect(fetches).toBe(afterNotFound)
  })

  it('stops polling once no row is pending', async () => {
    rows = [OFFHOURS_ASLEEP]
    renderHook(() => useOffHoursSchedule(1), { wrapper })
    await tick(100)
    const afterFirstLoad = fetches
    expect(afterFirstLoad).toBe(1)
    await tick(LIVE_REFETCH_MS * 4)
    expect(fetches).toBe(afterFirstLoad)
  })

  it('does not poll while the tab is hidden', async () => {
    renderHook(() => useOffHoursSchedule(1), { wrapper })
    await tick(100)
    visibility = 'hidden'
    const before = fetches
    await tick(LIVE_REFETCH_MS * 4)
    expect(fetches).toBe(before)
  })
})
