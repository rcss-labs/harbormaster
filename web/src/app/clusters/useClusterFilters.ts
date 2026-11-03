import { useCallback, useMemo } from 'react'
import { useSearchParams } from 'react-router'
import type { SortState } from '../../components/DataTable'
import { useFilters } from '../shell/useFilters'
import { useAuth } from '../auth/AuthProvider'
import { visibleTagIds } from '../shell/useFilters'
import { ALL_TAGS, resolveTagFilter, type ClusterPageFilters, type ClusterStatusFilter, type ClusterTagFilter } from './filterClusters'

export const CLUSTER_STATUS_FILTERS: ClusterStatusFilter[] = ['any', 'problems', 'deploying', 'healthy']
export const SORT_KEYS = ['name', 'status', 'services', 'tasks', 'cpu', 'mem'] as const

/** Status first, worst cluster on top. */
export const DEFAULT_SORT: SortState = { key: 'status', dir: 'desc' }

/** Status values written by earlier versions of the page (?status=needs_attention). */
const LEGACY_STATUS: Record<string, ClusterStatusFilter> = { needs_attention: 'problems', degraded: 'problems' }

function parseStatus(value: string | null): ClusterStatusFilter {
  if (value && (CLUSTER_STATUS_FILTERS as string[]).includes(value)) return value as ClusterStatusFilter
  return (value && LEGACY_STATUS[value]) || 'any'
}

function parseSort(value: string | null): SortState {
  const [key, dir] = (value ?? '').split(':')
  return (SORT_KEYS as readonly string[]).includes(key) && (dir === 'asc' || dir === 'desc') ? { key, dir } : DEFAULT_SORT
}

/** Tag (by id; the param keeps its old name env), status, text, sort and page of the Clusters list, kept in the URL (?env=&status=&q=&sort=key:dir&page=). */
export function useClusterFilters() {
  const [searchParams, setSearchParams] = useSearchParams()
  const { setAccount, setRegion, setTags } = useFilters()
  const { me } = useAuth()
  const envParam = searchParams.get('env')
  const tag: ClusterTagFilter = useMemo(() => {
    if (!me) return ALL_TAGS
    const visible = new Set(visibleTagIds(me))
    return resolveTagFilter(envParam, me.tags.filter((t) => visible.has(t.id)))
  }, [me, envParam])
  const status = parseStatus(searchParams.get('status'))
  const q = searchParams.get('q') ?? ''
  const sortParam = searchParams.get('sort')
  const sort = useMemo(() => parseSort(sortParam), [sortParam])
  const pageNumber = Number(searchParams.get('page'))
  const page = Number.isInteger(pageNumber) && pageNumber > 1 ? pageNumber : 1
  const filters = useMemo<ClusterPageFilters>(() => ({ tag, status, q }), [tag, status, q])

  const update = useCallback(
    (changes: Record<string, string | null>) =>
      setSearchParams(
        (current) => {
          const next = new URLSearchParams(current)
          for (const [key, value] of Object.entries(changes)) {
            if (value === null || value === '') next.delete(key)
            else next.set(key, value)
          }
          return next
        },
        { replace: true },
      ),
    [setSearchParams],
  )

  // Any change to what is listed returns to the first page.
  const setTag = useCallback((value: ClusterTagFilter) => update({ env: value === ALL_TAGS ? null : value, page: null }), [update])
  const setStatus = useCallback((value: ClusterStatusFilter) => update({ status: value === 'any' ? null : value, page: null }), [update])
  const setQ = useCallback((value: string) => update({ q: value, page: null }), [update])
  const setSort = useCallback(
    (value: SortState) => update({ sort: value.key === DEFAULT_SORT.key && value.dir === DEFAULT_SORT.dir ? null : `${value.key}:${value.dir}`, page: null }),
    [update],
  )
  const setPage = useCallback((value: number) => update({ page: value > 1 ? String(value) : null }), [update])
  const clearAll = useCallback(() => {
    // The shared setters clear the remembered scope; the final update then empties the URL in one go.
    setAccount(undefined)
    setRegion(undefined)
    setTags([])
    setSearchParams(
      (current) => {
        const next = new URLSearchParams(current)
        for (const key of ['env', 'status', 'q', 'page', 'account', 'region', 'tag']) next.delete(key)
        return next
      },
      { replace: true },
    )
  }, [setSearchParams, setAccount, setRegion, setTags])

  return { filters, sort, page, setTag, setStatus, setQ, setSort, setPage, clearAll }
}
