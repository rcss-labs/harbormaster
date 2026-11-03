import { useCallback, useMemo, useSyncExternalStore } from 'react'
import { useAuth } from '../auth/AuthProvider'

/** Harbormaster has no server-side favourites, so pins live in this browser, per user. */
export interface PinnedService {
  kind: 'service'
  id: string
  name: string
  cluster: string
}

export interface PinnedCluster {
  kind: 'cluster'
  id: string
  name: string
  account_name: string
  account_id: string
}

export type Pin = PinnedService | PinnedCluster
export type PinKind = Pin['kind']

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

const isText = (value: unknown): value is string => typeof value === 'string'

/** Entries saved before clusters could be pinned have no `kind`; they are services. */
function toPin(item: unknown): Pin | null {
  if (typeof item !== 'object' || item === null) return null
  const entry = item as Record<string, unknown>
  if (!isText(entry.id) || !isText(entry.name)) return null
  if (entry.kind === 'cluster') {
    return isText(entry.account_name) && isText(entry.account_id)
      ? { kind: 'cluster', id: entry.id, name: entry.name, account_name: entry.account_name, account_id: entry.account_id }
      : null
  }
  if (entry.kind !== undefined && entry.kind !== 'service') return null
  return isText(entry.cluster) ? { kind: 'service', id: entry.id, name: entry.name, cluster: entry.cluster } : null
}

function parse(raw: string): Pin[] {
  try {
    const value: unknown = JSON.parse(raw)
    if (!Array.isArray(value)) return []
    return value.map(toPin).filter((pin): pin is Pin => pin !== null)
  } catch {
    return []
  }
}

export function usePins() {
  const username = useAuth().me?.username ?? ''
  const raw = useSyncExternalStore(subscribe, () => readRaw(username))
  const pinned = useMemo(() => parse(raw), [raw])

  const write = useCallback(
    (next: Pin[]) => {
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
  const isPinned = useCallback((kind: PinKind, id: string) => pinned.some((p) => p.kind === kind && p.id === id), [pinned])
  const toggle = useCallback(
    (pin: Pin) =>
      write(isPinned(pin.kind, pin.id) ? pinned.filter((p) => !(p.kind === pin.kind && p.id === pin.id)) : [...pinned, pin]),
    [pinned, isPinned, write],
  )
  return { pins: pinned, isPinned, toggle }
}
