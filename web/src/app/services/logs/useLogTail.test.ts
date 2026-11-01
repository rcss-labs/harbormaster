import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useLogTail } from './useLogTail'

class MockSocket {
  static instances: MockSocket[] = []
  static OPEN = 1
  readyState = 0
  sent: string[] = []
  onopen: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  onclose: ((event: { code: number; reason: string }) => void) | null = null
  onerror: (() => void) | null = null
  constructor(public url: string) {
    MockSocket.instances.push(this)
  }
  send(data: string) {
    this.sent.push(data)
  }
  close() {
    this.readyState = 3
  }
  open() {
    this.readyState = 1
    this.onopen?.()
  }
  frame(payload: object) {
    this.onmessage?.({ data: JSON.stringify(payload) })
  }
  serverClose(code: number, reason = '') {
    this.readyState = 3
    this.onclose?.({ code, reason })
  }
}

const line = (message: string) => ({ t: '2026-10-31T10:00:00Z', container: 'app', task_id: 'a1b2c3d4e5f6', message })
const base = { serviceId: 'svc-1', container: undefined, q: '', enabled: true, csrf: 'tok' }

beforeEach(() => {
  MockSocket.instances = []
  vi.stubGlobal('WebSocket', MockSocket)
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('useLogTail', () => {
  it('sends start with csrf then appends lines', () => {
    const { result } = renderHook(() => useLogTail({ ...base, container: 'app', q: 'boom' }))
    const socket = MockSocket.instances[0]
    expect(socket.url).toMatch(/^wss?:\/\/.+\/api\/services\/svc-1\/logs\/tail\?container=app&q=boom$/)
    expect(result.current.status).toBe('connecting')
    act(() => socket.open())
    expect(JSON.parse(socket.sent[0])).toEqual({ type: 'start', csrf: 'tok' })
    act(() => socket.frame({ type: 'ready', group: '/ecs/x', prefixes: [] }))
    expect(result.current.status).toBe('live')
    act(() => socket.frame({ type: 'lines', items: [line('one'), line('two')] }))
    act(() => socket.frame({ type: 'lines', items: [line('three')] }))
    expect(result.current.lines.map((l) => l.message)).toEqual(['one', 'two', 'three'])
  })

  it('caps memory at 5000 lines, dropping the oldest', () => {
    const { result } = renderHook(() => useLogTail(base))
    const socket = MockSocket.instances[0]
    act(() => socket.open())
    act(() => socket.frame({ type: 'lines', items: Array.from({ length: 5003 }, (_, i) => line(`m${i}`)) }))
    expect(result.current.lines).toHaveLength(5000)
    expect(result.current.lines[0].message).toBe('m3')
  })

  it('end frame sets status ended and resume reconnects', () => {
    const { result } = renderHook(() => useLogTail(base))
    const first = MockSocket.instances[0]
    act(() => first.open())
    act(() => first.frame({ type: 'ready', group: 'g', prefixes: [] }))
    act(() => first.frame({ type: 'end', reason: 'idle', message: 'idle' }))
    act(() => first.serverClose(4408, 'idle'))
    expect(result.current.status).toBe('ended')
    expect(result.current.endReason).toBe('idle')
    act(() => result.current.resume())
    expect(MockSocket.instances).toHaveLength(2)
    expect(result.current.status).toBe('connecting')
    expect(result.current.endReason).toBeUndefined()
  })

  it('reports tail_limit from the close code with the human reason', () => {
    const { result } = renderHook(() => useLogTail(base))
    const socket = MockSocket.instances[0]
    act(() => socket.open())
    act(() => socket.serverClose(4409, 'You already have 2 live tails open.'))
    expect(result.current.status).toBe('ended')
    expect(result.current.endReason).toBe('tail_limit')
    expect(result.current.endMessage).toBe('You already have 2 live tails open.')
  })

  it('treats a 4502 close before ready as an error end', () => {
    const { result } = renderHook(() => useLogTail(base))
    const socket = MockSocket.instances[0]
    act(() => socket.open())
    act(() => socket.serverClose(4502, 'aws_error'))
    expect(result.current.status).toBe('ended')
    expect(result.current.endReason).toBe('error')
  })

  it('does not connect when disabled and closes the socket when disabled later', () => {
    const { result, rerender } = renderHook((props) => useLogTail(props), { initialProps: { ...base, enabled: false } })
    expect(MockSocket.instances).toHaveLength(0)
    expect(result.current.status).toBe('idle')
    rerender({ ...base, enabled: true })
    const socket = MockSocket.instances[0]
    rerender({ ...base, enabled: false })
    expect(socket.readyState).toBe(3)
    expect(result.current.status).toBe('idle')
  })

  it('keepalive at most once per minute', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-31T10:00:00Z'))
    const { result } = renderHook(() => useLogTail(base))
    const socket = MockSocket.instances[0]
    act(() => socket.open())
    const keepalives = () => socket.sent.filter((s) => JSON.parse(s).type === 'keepalive').length
    act(() => result.current.keepalive())
    act(() => result.current.keepalive())
    expect(keepalives()).toBe(1)
    vi.advanceTimersByTime(59_000)
    act(() => result.current.keepalive())
    expect(keepalives()).toBe(1)
    vi.advanceTimersByTime(2_000)
    act(() => result.current.keepalive())
    expect(keepalives()).toBe(2)
  })
})
