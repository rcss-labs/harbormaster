import { useCallback, useMemo } from 'react'
import { useSearchParams } from 'react-router'
import {
  SERVICE_SORT_KEYS,
  STATUS_FILTERS,
  VIEW_ALL,
  type ServiceSortKey,
  type ServiceStatusFilter,
  type ServiceView,
  type SortDir,
} from './filterServices'

/** Status first, worst service on top. */
export const DEFAULT_SORT: { key: ServiceSortKey; dir: SortDir } = { key: 'status', dir: 'desc' }

/** View, status, text, sort and page of the Services list, kept in the URL (?view=&status=&q=&sort=&dir=&page=). */
export function useServicesState() {
  const [searchParams, setSearchParams] = useSearchParams()
  const view: ServiceView = searchParams.get('view') || VIEW_ALL
  const statusParam = searchParams.get('status')
  const status: ServiceStatusFilter = (STATUS_FILTERS as string[]).includes(statusParam ?? '') ? (statusParam as ServiceStatusFilter) : 'any'
  const q = searchParams.get('q') ?? ''
  const sortParam = searchParams.get('sort')
  const dirParam = searchParams.get('dir')
  const sort = useMemo(() => {
    const key = (SERVICE_SORT_KEYS as string[]).includes(sortParam ?? '') ? (sortParam as ServiceSortKey) : DEFAULT_SORT.key
    const dir: SortDir = dirParam === 'asc' || dirParam === 'desc' ? dirParam : DEFAULT_SORT.dir
    return { key, dir }
  }, [sortParam, dirParam])
  const pageNumber = Number(searchParams.get('page'))
  const page = Number.isInteger(pageNumber) && pageNumber > 1 ? pageNumber : 1

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
  const setView = useCallback((value: ServiceView) => update({ view: value === VIEW_ALL ? null : value, page: null }), [update])
  const setStatus = useCallback((value: ServiceStatusFilter) => update({ status: value === 'any' ? null : value, page: null }), [update])
  const setQ = useCallback((value: string) => update({ q: value, page: null }), [update])
  const setSort = useCallback((value: { key: string; dir: SortDir }) => update({ sort: value.key, dir: value.dir, page: null }), [update])
  const setPage = useCallback((value: number) => update({ page: value > 1 ? String(value) : null }), [update])
  const clearAll = useCallback(() => update({ view: null, status: null, q: null, page: null }), [update])

  return { view, status, q, sort, page, setView, setStatus, setQ, setSort, setPage, clearAll }
}
