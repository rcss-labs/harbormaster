import { cleanup, renderHook } from '@testing-library/react'
import { setTimeout as realSleep } from 'node:timers/promises'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { server } from '../test/msw'
import { FOCUS_LIVE } from '../test/focusFixtures'
import { FOCUS_HEARTBEAT_MS, useFocus, type FocusDTO } from './focus'

interface Seen {
  method: string
  body: { kind: string; id: string; open?: boolean }
}

let seen: Seen[] = []
let visibility: 'visible' | 'hidden' = 'visible'

function setVisibility(state: 'visible' | 'hidden') {
  visibility = state
  document.dispatchEvent(new Event('visibilitychange'))
}

beforeEach(() => {
  seen = []
  visibility = 'visible'
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => visibility })
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] })
  server.use(
    http.post('/api/focus', async ({ request }) => {
      seen.push({ method: 'POST', body: (await request.json()) as Seen['body'] })
      return HttpResponse.json(FOCUS_LIVE)
    }),
    http.delete('/api/focus', async ({ request }) => {
      seen.push({ method: 'DELETE', body: (await request.json()) as Seen['body'] })
      return new HttpResponse(null, { status: 204 })
    }),
  )
})

afterEach(async () => {
  cleanup() // unmount releases the object; let that request land before the next test resets `seen`
  await realSleep(20)
  vi.useRealTimers()
  Reflect.deleteProperty(document, 'visibilityState')
})

const posts = () => seen.filter((s) => s.method === 'POST')
const deletes = () => seen.filter((s) => s.method === 'DELETE')
/** Advances the fake timers, then lets the requests they caused finish (the network is real, so wait on the real clock). */
const tick = async (ms: number) => {
  await vi.advanceTimersByTimeAsync(ms)
  await realSleep(15)
}

describe('useFocus', () => {
  it('opens on mount and refetches at 1, 2 and 3 s', async () => {
    const refetch = vi.fn().mockResolvedValue(undefined)
    const { result } = renderHook(() => useFocus('service', 'svc-1', refetch))
    await tick(0)
    expect(posts()).toEqual([{ method: 'POST', body: { kind: 'service', id: 'svc-1', open: true } }])
    expect(result.current).toEqual(FOCUS_LIVE)
    expect(refetch).not.toHaveBeenCalled()
    await tick(1000)
    expect(refetch).toHaveBeenCalledTimes(1)
    await tick(1000)
    expect(refetch).toHaveBeenCalledTimes(2)
    await tick(1000)
    expect(refetch).toHaveBeenCalledTimes(3)
    await tick(2000)
    expect(refetch).toHaveBeenCalledTimes(3)
  })

  it('asks again 3 s after an open, so the pill shows the first live read without waiting a heartbeat', async () => {
    renderHook(() => useFocus('service', 'svc-1', vi.fn().mockResolvedValue(undefined)))
    await tick(0)
    expect(posts()).toHaveLength(1)
    await tick(3000)
    expect(posts().map((p) => p.body.open)).toEqual([true, false])
    await tick(FOCUS_HEARTBEAT_MS - 3000)
    expect(posts()).toHaveLength(3) // the regular heartbeat is on its own 10 s clock
  })

  it('heartbeats every 10 s only while visible', async () => {
    renderHook(() => useFocus('cluster', 'cl-1', vi.fn().mockResolvedValue(undefined)))
    await tick(0)
    expect(posts()).toHaveLength(1)
    await tick(FOCUS_HEARTBEAT_MS) // the recheck after the open, then the first heartbeat
    expect(posts()).toHaveLength(3)
    expect(posts()[2].body.open).toBe(false)
    await tick(FOCUS_HEARTBEAT_MS)
    expect(posts()).toHaveLength(4)

    setVisibility('hidden')
    await tick(5 * FOCUS_HEARTBEAT_MS)
    expect(posts()).toHaveLength(4)
  })

  it('releases on hide and on unmount', async () => {
    const { unmount } = renderHook(() => useFocus('service', 'svc-1', vi.fn().mockResolvedValue(undefined)))
    await tick(0)
    setVisibility('hidden')
    await tick(0)
    expect(deletes()).toEqual([{ method: 'DELETE', body: { kind: 'service', id: 'svc-1', open: false } }])

    setVisibility('visible')
    await tick(0)
    unmount()
    await tick(0)
    expect(deletes()).toHaveLength(2)
    expect(deletes()[1].body).toMatchObject({ kind: 'service', id: 'svc-1' })
  })

  it('opens again when the tab becomes visible', async () => {
    const refetch = vi.fn().mockResolvedValue(undefined)
    renderHook(() => useFocus('service', 'svc-1', refetch))
    await tick(0)
    setVisibility('hidden')
    await tick(0)
    refetch.mockClear()
    setVisibility('visible')
    await tick(0)
    expect(posts().map((p) => p.body.open)).toEqual([true, true])
    await tick(3000)
    expect(refetch).toHaveBeenCalledTimes(3)
  })

  it('does nothing while the tab starts hidden, and opens when it is shown', async () => {
    visibility = 'hidden'
    renderHook(() => useFocus('service', 'svc-1', vi.fn().mockResolvedValue(undefined)))
    await tick(FOCUS_HEARTBEAT_MS)
    expect(seen).toEqual([])
    setVisibility('visible')
    await tick(0)
    expect(posts()).toHaveLength(1)
  })

  it('switches objects when the id changes', async () => {
    const refetch = vi.fn().mockResolvedValue(undefined)
    const { rerender } = renderHook(({ id }) => useFocus('service', id, refetch), { initialProps: { id: 'svc-1' } })
    await tick(0)
    rerender({ id: 'svc-2' })
    await tick(0)
    expect(deletes().map((d) => d.body.id)).toEqual(['svc-1'])
    expect(posts().map((p) => [p.body.id, p.body.open])).toEqual([
      ['svc-1', true],
      ['svc-2', true],
    ])
  })

  it('shows no warning while the first live read is still pending, then reports unavailable', async () => {
    const waiting: FocusDTO = { state: 'unavailable', interval_seconds: 30, refreshed_at: null, message: 'Live refresh unavailable' }
    server.use(http.post('/api/focus', () => HttpResponse.json(waiting)))
    const { result } = renderHook(() => useFocus('service', 'svc-1', vi.fn().mockResolvedValue(undefined)))
    await tick(3000) // the recheck: nothing is wrong yet
    expect(result.current?.state).not.toBe('unavailable')
    expect(result.current?.refreshed_at).toBeNull()
    expect(result.current?.message).toBe('')
    await tick(2 * FOCUS_HEARTBEAT_MS) // well past the open, still no evidence: now it is a fault
    expect(result.current?.state).toBe('unavailable')
  })

  it('reports unavailable when the request fails, and keeps heartbeating', async () => {
    server.use(http.post('/api/focus', () => HttpResponse.json({ error: { code: 'x', message: 'down' } }, { status: 500 })))
    const { result } = renderHook(() => useFocus('service', 'svc-1', vi.fn().mockResolvedValue(undefined)))
    await tick(0)
    expect(result.current?.state).toBe('unavailable')
    expect(result.current?.message).toBe('Live refresh unavailable')

    server.use(http.post('/api/focus', () => HttpResponse.json(FOCUS_LIVE)))
    await tick(FOCUS_HEARTBEAT_MS)
    expect(result.current?.state).toBe('live')
  })
})
