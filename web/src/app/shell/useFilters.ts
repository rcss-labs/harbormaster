import { useCallback, useEffect, useMemo, useSyncExternalStore } from 'react'
import { useSearchParams } from 'react-router'
import type { Filters } from '../../api/hooks'
import type { MeDTO } from '../../api/types'
import { VIEW_BIT } from '../admin/perms'
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

/** Tag ids this user may filter by: every tag for admins, otherwise those they hold the View bit on. */
export function visibleTagIds(me: MeDTO): number[] {
  if (me.admin) return me.tags.map((tag) => tag.id)
  return me.tags.filter((tag) => ((me.perms[String(tag.id)] ?? 0) & VIEW_BIT) !== 0).map((tag) => tag.id)
}

const uniqueNumbers = (values: unknown[]): number[] | undefined => {
  const numbers = [...new Set(values.filter((v): v is number => typeof v === 'number' && Number.isInteger(v)))].sort((a, b) => a - b)
  return numbers.length > 0 ? numbers : undefined
}

function parseStored(raw: string): Filters {
  try {
    const parsed: unknown = JSON.parse(raw)
    if (parsed && typeof parsed === 'object') {
      const { account, region, tag } = parsed as Record<string, unknown>
      return {
        account: typeof account === 'string' ? account : undefined,
        region: typeof region === 'string' ? region : undefined,
        tag: Array.isArray(tag) ? uniqueNumbers(tag) : undefined,
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
  const visible = new Set(visibleTagIds(me))
  return {
    account: me.accounts.some((a) => a.account_id === stored.account) ? stored.account : undefined,
    region: me.regions.includes(stored.region ?? '') ? stored.region : undefined,
    tag: uniqueNumbers((stored.tag ?? []).filter((id) => visible.has(id))),
  }
}

export interface FilterState {
  filters: Filters
  setAccount: (account: string | undefined) => void
  setRegion: (region: string | undefined) => void
  setTags: (ids: number[]) => void
}

/**
 * Account/region/tag filter shared by the sidebar, top bar and screens.
 * The URL (?account=&region=&tag=) wins; without it the user's remembered, still-valid choice applies.
 */
export function useFilters(): FilterState {
  const { me } = useAuth()
  const [searchParams, setSearchParams] = useSearchParams()
  const username = me?.username
  const urlAccount = searchParams.get('account') ?? undefined
  const urlRegion = searchParams.get('region') ?? undefined
  // A string key keeps the array identity stable across renders.
  const urlTagKey = uniqueNumbers(searchParams.getAll('tag').filter((v) => /^\d+$/.test(v)).map(Number))?.join(',')

  const rawStored = useSyncExternalStore(subscribeToStorage, () => readRaw(username))
  const restored = useMemo(() => (me ? validOnly(parseStored(rawStored), me) : {}), [me, rawStored])
  const hasUrlFilter = urlAccount !== undefined || urlRegion !== undefined || searchParams.has('tag')
  const filters = useMemo<Filters>(
    () => (hasUrlFilter ? { account: urlAccount, region: urlRegion, tag: urlTagKey?.split(',').map(Number) } : restored),
    [hasUrlFilter, urlAccount, urlRegion, urlTagKey, restored],
  )

  // Navigating to a page without params (e.g. a sidebar link) re-applies the remembered filters to the URL.
  useEffect(() => {
    if (hasUrlFilter || (!restored.account && !restored.region && !restored.tag)) return
    setSearchParams(
      (current) => {
        const next = new URLSearchParams(current)
        if (restored.account) next.set('account', restored.account)
        if (restored.region) next.set('region', restored.region)
        for (const id of restored.tag ?? []) next.append('tag', String(id))
        return next
      },
      { replace: true },
    )
  }, [hasUrlFilter, restored, setSearchParams])

  const update = useCallback(
    (key: 'account' | 'region' | 'tag', value: string | number[] | undefined) => {
      if (!username) return
      const next: Filters = { ...filters, [key]: value }
      // Persist first so the restore effect cannot resurrect the value being cleared.
      writeStored(username, { account: next.account, region: next.region, tag: next.tag?.length ? next.tag : undefined })
      setSearchParams(
        (current) => {
          const params = new URLSearchParams(current)
          params.delete(key)
          if (Array.isArray(value)) for (const id of value) params.append(key, String(id))
          else if (value) params.set(key, value)
          // Keep the sibling filters explicit so "All" for one does not fall back to the remembered value of another.
          for (const other of ['account', 'region'] as const) {
            if (other !== key && next[other]) params.set(other, next[other])
          }
          if (key !== 'tag') {
            params.delete('tag')
            for (const id of next.tag ?? []) params.append('tag', String(id))
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
  const setTags = useCallback((ids: number[]) => update('tag', uniqueNumbers(ids) ?? []), [update])
  return { filters, setAccount, setRegion, setTags }
}
