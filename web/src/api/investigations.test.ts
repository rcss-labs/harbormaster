import { act, renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createElement, type ReactNode } from 'react'
import { MemoryRouter } from 'react-router'
import { http, HttpResponse } from 'msw'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ME_ADMIN } from '../test/fixtures'
import { server } from '../test/msw'
import { AuthProvider } from '../app/auth/AuthProvider'
import { INVESTIGATION_DONE, mockInvestigations, toListItem } from '../test/investigationFixtures'
import { useInvestigation, useServiceInvestigations } from './investigations'

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return createElement(QueryClientProvider, { client }, createElement(MemoryRouter, null, createElement(AuthProvider, null, children)))
}

describe('investigation hooks', () => {
  it('fetch the detail and the service list when the agent is on', async () => {
    const requested = mockInvestigations()
    const detail = renderHook(() => useInvestigation(INVESTIGATION_DONE.id), { wrapper })
    const list = renderHook(() => useServiceInvestigations(INVESTIGATION_DONE.subject.service_id), { wrapper })

    await waitFor(() => expect(detail.result.current.data?.id).toBe(INVESTIGATION_DONE.id))
    await waitFor(() => expect(list.result.current.data?.items).toHaveLength(1))
    expect(requested).toContain(`/api/investigations/${INVESTIGATION_DONE.id}`)
  })

  it('make no request when the agent is off', async () => {
    const requested = mockInvestigations([INVESTIGATION_DONE], false)
    const detail = renderHook(() => useInvestigation(INVESTIGATION_DONE.id), { wrapper })
    const list = renderHook(() => useServiceInvestigations(INVESTIGATION_DONE.subject.service_id), { wrapper })

    await new Promise((resolve) => setTimeout(resolve, 100))
    expect(detail.result.current.fetchStatus).toBe('idle')
    expect(list.result.current.fetchStatus).toBe('idle')
    expect(requested).toEqual([])
  })
})


describe('investigation polling', () => {
  afterEach(() => {
    vi.useRealTimers()
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' })
  })

  /** Serves `/api/investigations/:id` with the state the test sets, and counts the calls. */
  function serve(initial: string) {
    const control = { state: initial, calls: 0 }
    server.use(
      http.get('/api/me', () => HttpResponse.json({ ...ME_ADMIN, agent_enabled: true })),
      http.get('/api/investigations/:id', () => {
        control.calls++
        return HttpResponse.json({ ...INVESTIGATION_DONE, state: control.state })
      }),
      http.get('/api/services/:id/investigations', () => {
        control.calls++
        return HttpResponse.json({ items: [toListItem({ ...INVESTIGATION_DONE, state: control.state })] })
      }),
    )
    return control
  }
  const tick = (ms: number) => act(() => vi.advanceTimersByTimeAsync(ms))
  const start = async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] })
  }

  it('polls the view every 5 s while running and stops when done', async () => {
    const control = serve('running')
    await start()
    renderHook(() => useInvestigation(7), { wrapper })
    await tick(100)
    expect(control.calls).toBe(1)

    await tick(5000)
    expect(control.calls).toBe(2)
    await tick(5000)
    expect(control.calls).toBe(3)

    control.state = 'done'
    await tick(5000)
    expect(control.calls).toBe(4)
    await tick(30_000)
    expect(control.calls).toBe(4)
  })

  it('never polls an unknown state', async () => {
    const control = serve('paused')
    await start()
    renderHook(() => useInvestigation(7), { wrapper })
    await tick(100)
    await tick(30_000)

    expect(control.calls).toBe(1)
  })

  it('stops after 30 minutes', async () => {
    const control = serve('requested')
    await start()
    renderHook(() => useInvestigation(7), { wrapper })
    // Each step lets the previous fetch settle before the next interval is armed.
    for (let elapsed = 0; elapsed < 30 * 60_000 + 10_000; elapsed += 5000) await tick(5000)
    const after = control.calls
    await tick(60_000)

    expect(after).toBeGreaterThan(300)
    expect(after).toBeLessThan(400)
    expect(control.calls).toBe(after)
  })

  it('stops on unmount and while the tab is hidden', async () => {
    const control = serve('running')
    await start()
    const view = renderHook(() => useInvestigation(7), { wrapper })
    await tick(100)

    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' })
    act(() => void document.dispatchEvent(new Event('visibilitychange')))
    await tick(20_000)
    expect(control.calls).toBe(1)

    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' })
    act(() => void document.dispatchEvent(new Event('visibilitychange')))
    await tick(100)
    const before = control.calls
    view.unmount()
    await tick(30_000)
    expect(control.calls).toBe(before)
  })

  it('polls the service list every 15 s while it shows a running entry only', async () => {
    const control = serve('requested')
    await start()
    renderHook(() => useServiceInvestigations('svc'), { wrapper })
    await tick(100)
    expect(control.calls).toBe(1)
    await tick(5000)
    expect(control.calls).toBe(1)
    await tick(10_000)
    expect(control.calls).toBe(2)

    control.state = 'done'
    await tick(15_000)
    expect(control.calls).toBe(3)
    await tick(60_000)
    expect(control.calls).toBe(3)
  })
})
