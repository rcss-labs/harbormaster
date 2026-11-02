import { useCallback, useMemo, useSyncExternalStore } from 'react'
import { useAuth } from '../auth/AuthProvider'

/** A pinned service. Harbormaster has no server-side favourites, so pins live in this browser, per user. */
export interface PinnedService {
  id: string
  name: string
  cluster: string
}

const storageKey = (username: string) => `harbormaster-pinned:${username}`

const listeners = new Set<() => void>()
const subscribe = (listener: () => void) => {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

function readRaw(username: string): string {
  if (!username) return '[]'
  try {
    return localStorage.getItem(storageKey(username)) ?? '[]'
  } catch {
    return '[]'
  }
}

function parse(raw: string): PinnedService[] {
  try {
    const value: unknown = JSON.parse(raw)
    if (!Array.isArray(value)) return []
    return value.filter(
      (item): item is PinnedService =>
        typeof item?.id === 'string' && typeof item.name === 'string' && typeof item.cluster === 'string',
    )
  } catch {
    return []
  }
}

export function usePinnedServices() {
  const username = useAuth().me?.username ?? ''
  const raw = useSyncExternalStore(subscribe, () => readRaw(username))
  const pinned = useMemo(() => parse(raw), [raw])

  const write = useCallback(
    (next: PinnedService[]) => {
      if (!username) return
      try {
        localStorage.setItem(storageKey(username), JSON.stringify(next))
      } catch {
        // storage unavailable: pins last for this page view only
        return
      }
      listeners.forEach((listener) => listener())
    },
    [username],
  )
  const isPinned = useCallback((id: string) => pinned.some((p) => p.id === id), [pinned])
  const toggle = useCallback(
    (service: PinnedService) => write(isPinned(service.id) ? pinned.filter((p) => p.id !== service.id) : [...pinned, service]),
    [pinned, isPinned, write],
  )
  return { pinned, isPinned, toggle }
}
