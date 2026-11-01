import { useCallback, useMemo } from 'react'
import { useSearchParams } from 'react-router'
import type { ClusterStatus } from '../../api/types'
import { useFilters } from '../shell/useFilters'
import type { ClusterPageFilters } from './filterClusters'

export const CLUSTER_STATUSES: ClusterStatus[] = ['healthy', 'deploying', 'degraded', 'needs_attention']

const isClusterStatus = (value: string): value is ClusterStatus => (CLUSTER_STATUSES as string[]).includes(value)

/** Status and name filters of the Clusters page, kept in the URL (?status=&q=). */
export function useClusterFilters() {
  const [searchParams, setSearchParams] = useSearchParams()
  const { setAccount, setRegion, setTags } = useFilters()
  const statusKey = searchParams.getAll('status').filter(isClusterStatus).join(',')
  const q = searchParams.get('q') ?? ''
  const page = useMemo<ClusterPageFilters>(() => ({ status: statusKey ? (statusKey.split(',') as ClusterStatus[]) : [], q }), [statusKey, q])

  const setStatus = useCallback(
    (status: ClusterStatus[]) =>
      setSearchParams(
        (current) => {
          const next = new URLSearchParams(current)
          next.delete('status')
          for (const s of status) next.append('status', s)
          return next
        },
        { replace: true },
      ),
    [setSearchParams],
  )
  const setQ = useCallback(
    (value: string) =>
      setSearchParams(
        (current) => {
          const next = new URLSearchParams(current)
          if (value) next.set('q', value)
          else next.delete('q')
          return next
        },
        { replace: true },
      ),
    [setSearchParams],
  )
  const clearAll = useCallback(() => {
    // The shared setters clear the remembered scope; the final update then empties the URL (status, q and scope) in one go.
    setAccount(undefined)
    setRegion(undefined)
    setTags([])
    setSearchParams(
      (current) => {
        const next = new URLSearchParams(current)
        for (const key of ['status', 'q', 'account', 'region', 'tag']) next.delete(key)
        return next
      },
      { replace: true },
    )
  }, [setSearchParams, setAccount, setRegion, setTags])

  return { page, setStatus, setQ, clearAll }
}
