import { useCallback, useEffect, useMemo, useSyncExternalStore } from 'react'
import { useSearchParams } from 'react-router'
import type { Filters } from '../../api/hooks'
import type { MeDTO } from '../../api/types'
import { useAuth } from '../auth/AuthProvider'

const storageKey = (username: string) => `harbormaster-filters:${username}`

// Every useFilters instance (sidebar, top bar, screens) lives as long as the Shell, so they must all see writes.
const storageListeners = new Set<() => void>()
const subscribeToStorage = (listener: () => void) => {
  storageListeners.add(listener)
  return () => {
    storageListeners.delete(listener)
  }
}

function readRaw(username: string | undefined): string {
  if (!username) return '{}'
  try {
    return localStorage.getItem(storageKey(username)) ?? '{}'
  } catch {
    return '{}'
  }
}

function parseStored(raw: string): Filters {
  try {
    const parsed: unknown = JSON.parse(raw)
    if (parsed && typeof parsed === 'object') {
      const { account, region } = parsed as Record<string, unknown>
      return {
        account: typeof account === 'string' ? account : undefined,
        region: typeof region === 'string' ? region : undefined,
      }
    }
  } catch {
    // corrupt or unavailable storage: behave as if nothing was stored
  }
  return {}
}

function writeStored(username: string, filters: Filters) {
  try {
    localStorage.setItem(storageKey(username), JSON.stringify(filters))
  } catch {
    // storage unavailable: the filter still lives in the URL
  }
  storageListeners.forEach((listener) => listener())
}

/** Drops stored values that no longer exist in what this user can see. */
function validOnly(stored: Filters, me: MeDTO): Filters {
  return {
    account: me.accounts.some((a) => a.account_id === stored.account) ? stored.account : undefined,
    region: me.regions.includes(stored.region ?? '') ? stored.region : undefined,
  }
}

export interface FilterState {
  filters: Filters
  setAccount: (account: string | undefined) => void
  setRegion: (region: string | undefined) => void
}

/**
 * Account/region filter shared by the sidebar, top bar and screens.
 * The URL (?account=&region=) wins; without it the user's remembered, still-valid choice applies.
 */
export function useFilters(): FilterState {
  const { me } = useAuth()
  const [searchParams, setSearchParams] = useSearchParams()
  const username = me?.username
  const urlAccount = searchParams.get('account') ?? undefined
  const urlRegion = searchParams.get('region') ?? undefined

  const rawStored = useSyncExternalStore(subscribeToStorage, () => readRaw(username))
  const restored = useMemo(() => (me ? validOnly(parseStored(rawStored), me) : {}), [me, rawStored])
  const hasUrlFilter = urlAccount !== undefined || urlRegion !== undefined
  const filters = useMemo<Filters>(
    () => (hasUrlFilter ? { account: urlAccount, region: urlRegion } : restored),
    [hasUrlFilter, urlAccount, urlRegion, restored],
  )

  // Navigating to a page without params (e.g. a sidebar link) re-applies the remembered filters to the URL.
  useEffect(() => {
    if (hasUrlFilter || (!restored.account && !restored.region)) return
    setSearchParams(
      (current) => {
        const next = new URLSearchParams(current)
        if (restored.account) next.set('account', restored.account)
        if (restored.region) next.set('region', restored.region)
        return next
      },
      { replace: true },
    )
  }, [hasUrlFilter, restored, setSearchParams])

  const update = useCallback(
    (key: 'account' | 'region', value: string | undefined) => {
      if (!username) return
      const next: Filters = { ...filters, [key]: value }
      // Persist first so the restore effect cannot resurrect the value being cleared.
      writeStored(username, { account: next.account, region: next.region })
      setSearchParams(
        (current) => {
          const params = new URLSearchParams(current)
          if (value) params.set(key, value)
          else params.delete(key)
          // Keep the sibling filter explicit so "All" for one does not fall back to the remembered value of the other.
          for (const other of ['account', 'region'] as const) {
            if (other !== key && next[other]) params.set(other, next[other])
          }
          return params
        },
        { replace: true },
      )
    },
    [filters, setSearchParams, username],
  )

  const setAccount = useCallback((account: string | undefined) => update('account', account), [update])
  const setRegion = useCallback((region: string | undefined) => update('region', region), [update])
  return { filters, setAccount, setRegion }
}
