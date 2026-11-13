import { act, renderHook } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { usePins, type Pin } from './pins'

let username = 'sam'
vi.mock('../auth/AuthProvider', () => ({ useAuth: () => ({ me: { username } }) }))

const service: Pin = { kind: 'service', id: 's1', name: 'payments-api', cluster: 'core-services' }
const cluster: Pin = { kind: 'cluster', id: 'c1', name: 'core-services', account_name: 'homelab-admin', account_id: '482109375512' }

beforeEach(() => {
  localStorage.clear()
  username = 'sam'
})

describe('usePins', () => {
  it('toggles services and clusters independently, in pin order', () => {
    const { result } = renderHook(() => usePins())
    act(() => result.current.toggle(cluster))
    act(() => result.current.toggle(service))
    expect(result.current.pins).toEqual([cluster, service])
    expect(result.current.isPinned('cluster', 'c1')).toBe(true)
    expect(result.current.isPinned('service', 'c1')).toBe(false)

    act(() => result.current.toggle(cluster))
    expect(result.current.pins).toEqual([service])
    expect(result.current.isPinned('cluster', 'c1')).toBe(false)
  })

  it('removes one pin by kind and id and keeps a pin of the other kind with the same id', () => {
    const { result } = renderHook(() => usePins())
    act(() => result.current.toggle({ ...service, id: 'same' }))
    act(() => result.current.toggle({ ...cluster, id: 'same' }))
    act(() => result.current.remove('service', 'same'))
    expect(result.current.pins).toEqual([{ ...cluster, id: 'same' }])
    expect(JSON.parse(localStorage.getItem('harbormaster-pinned:sam')!)).toEqual([{ ...cluster, id: 'same' }])
  })

  it('keys a service and a cluster with the same id separately', () => {
    const { result } = renderHook(() => usePins())
    act(() => result.current.toggle({ ...service, id: 'same' }))
    act(() => result.current.toggle({ ...cluster, id: 'same' }))
    expect(result.current.pins).toHaveLength(2)
  })

  it('reads entries stored before pins had a kind as services', () => {
    localStorage.setItem('harbormaster-pinned:sam', JSON.stringify([{ id: 'abc', name: 'edge-nginx', cluster: 'edge-proxy' }]))
    const { result } = renderHook(() => usePins())
    expect(result.current.pins).toEqual([{ kind: 'service', id: 'abc', name: 'edge-nginx', cluster: 'edge-proxy' }])
    expect(result.current.isPinned('service', 'abc')).toBe(true)
  })

  it('ignores malformed entries', () => {
    localStorage.setItem('harbormaster-pinned:sam', JSON.stringify([{ kind: 'cluster', id: 'x' }, 5, service]))
    const { result } = renderHook(() => usePins())
    expect(result.current.pins).toEqual([service])
  })

  it('stores pins per user', () => {
    const { result } = renderHook(() => usePins())
    act(() => result.current.toggle(cluster))
    expect(JSON.parse(localStorage.getItem('harbormaster-pinned:sam')!)).toEqual([cluster])
    username = 'omer'
    const other = renderHook(() => usePins())
    expect(other.result.current.pins).toEqual([])
  })

  describe('move', () => {
    const second: Pin = { ...service, id: 's2', name: 'orders-api' }
    const seed = () => {
      const hook = renderHook(() => usePins())
      act(() => hook.result.current.toggle(service))
      act(() => hook.result.current.toggle(cluster))
      act(() => hook.result.current.toggle(second))
      return hook
    }
    const stored = () => JSON.parse(localStorage.getItem('harbormaster-pinned:sam')!)

    it('moves a pin to a new index across kinds and persists the order', () => {
      const { result } = seed()
      act(() => result.current.move('service', 's2', 0))
      expect(result.current.pins).toEqual([second, service, cluster])
      expect(stored()).toEqual([second, service, cluster])
      act(() => result.current.move('service', 's1', 2))
      expect(result.current.pins).toEqual([second, cluster, service])
    })

    it('keeps the order when moved to its own place, out of range or for an unknown pin', () => {
      const { result } = seed()
      act(() => result.current.move('cluster', 'c1', 1))
      act(() => result.current.move('cluster', 'c1', -1))
      act(() => result.current.move('cluster', 'c1', 3))
      act(() => result.current.move('cluster', 'missing', 0))
      expect(result.current.pins).toEqual([service, cluster, second])
    })

    it('identifies the pin by kind as well as id', () => {
      const { result } = renderHook(() => usePins())
      act(() => result.current.toggle({ ...service, id: 'same' }))
      act(() => result.current.toggle({ ...cluster, id: 'same' }))
      act(() => result.current.move('cluster', 'same', 0))
      expect(result.current.pins.map((p) => p.kind)).toEqual(['cluster', 'service'])
    })
  })
})
