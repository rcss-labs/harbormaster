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
})
