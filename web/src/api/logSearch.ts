// Types and hooks for the Log search endpoints. They mirror the Go DTOs (JSON is snake_case).
import { useEffect, useRef } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, apiFetch } from './client'
import type { ListDTO } from './types'

export type LogSearchRange = '15m' | '1h' | '24h' | 'custom'

export interface LogSearchServiceDTO {
  id: string
  name: string
  cluster: string
  account_id: string
  account_name: string
  region: string
}

export interface LogSearchRequest {
  service_ids: string[]
  range: LogSearchRange
  /** RFC 3339; only for the `custom` range. */
  from?: string
  to?: string
  query: string
}

export interface LogSearchRowDTO {
  t: string
  service_id: string
  container: string
  task_id: string
  message: string
  message_truncated: boolean
}

export type LogSearchServiceState = 'ok' | 'no_awslogs' | 'target_failed'

export interface LogSearchServiceResultDTO {
  id: string
  name: string
  state: LogSearchServiceState
  message: string
}

export type LogSearchTargetState = 'ok' | 'no_permission' | 'throttled' | 'timeout' | 'cancelled' | 'error' | 'scan_limit'

export interface LogSearchTargetDTO {
  account_id: string
  account_name: string
  region: string
  state: LogSearchTargetState
  message: string
  bytes_scanned: number
  records_matched: number
}

export interface LogSearchResultDTO {
  rows: LogSearchRowDTO[]
  records_matched: number
  bytes_scanned: number
  /** True when the row cap cut the result; more records matched than rows returned. */
  capped: boolean
  /** True when a query was stopped at the scan limit; the rows are partial. */
  scan_limit_reached: boolean
  /** The scan limit in bytes; 0 means there is none. */
  scan_limit_bytes: number
  cost_usd: number
  duration_ms: number
  from: string
  to: string
  services: LogSearchServiceResultDTO[]
  targets: LogSearchTargetDTO[]
}

/** An upper bound on what a search may scan, from the stored size of its log groups (not the time range). */
export interface LogSearchEstimateDTO {
  stored_bytes: number
  cost_usd: number
  groups: number
  /** Log groups whose size could not be read; they are not in `stored_bytes`. */
  unknown_groups: number
  /** The scan limit in bytes; 0 means there is none. */
  scan_limit_bytes: number
  exceeds_scan_limit: boolean
}

export interface SavedLogSearchDTO {
  id: string
  name: string
  query: string
  service_ids: string[]
  /** Saved service ids the caller may no longer see; the server leaves them out of `service_ids`. */
  hidden_services: number
  range: LogSearchRange
  from: string | null
  to: string | null
  updated_at: string
}

export interface SaveLogSearchBody {
  name: string
  query: string
  service_ids: string[]
  range: LogSearchRange
  from?: string | null
  to?: string | null
}

const SERVICES_STALE_MS = 60_000
const SAVED_KEY = ['log-search', 'saved'] as const
const LAST_KEY = ['log-search', 'last'] as const

/** The most recent successful run, kept in the query cache so the page can show it again after a round trip (D10). */
export interface LastLogSearch {
  request: LogSearchRequest
  result: LogSearchResultDTO
  submittedAt: number
}

/** Services the caller may search, with the account and region each log group is read from. */
export function useLogSearchServices() {
  return useQuery({
    queryKey: ['log-search', 'services'],
    queryFn: () => api.get<ListDTO<LogSearchServiceDTO>>('/api/log-search/services'),
    staleTime: SERVICES_STALE_MS,
  })
}

/**
 * Runs a search. The request carries an AbortSignal and is aborted when the page unmounts or `cancel()` is
 * called, so a search nobody waits for does not keep scanning on the server. Errors are ApiErrors
 * (422, 403, 404, 409, 429 use the app's error body). Cancel or unmount rejects with an AbortError, which the page should not show as a failure.
 */
export function useRunLogSearch() {
  const queryClient = useQueryClient()
  const controller = useRef<AbortController | null>(null)
  const mutation = useMutation({
    mutationKey: ['log-search', 'run'],
    mutationFn: (body: LogSearchRequest) => {
      controller.current?.abort()
      const own = new AbortController()
      controller.current = own
      return apiFetch<LogSearchResultDTO>('/api/log-search', { method: 'POST', body: JSON.stringify(body), signal: own.signal })
    },
    onSuccess: (result, request) => {
      // Never evicted by time: the next run replaces it and signing out clears the whole cache.
      queryClient.setQueryDefaults(LAST_KEY, { gcTime: Infinity })
      queryClient.setQueryData<LastLogSearch>(LAST_KEY, { request, result, submittedAt: Date.now() })
    },
  })
  useEffect(() => () => controller.current?.abort(), [])
  return { ...mutation, cancel: () => controller.current?.abort() }
}

/**
 * The kept result of the last run when it answered exactly this request, else undefined. It never runs a
 * search: opening a link only fills the form (D8), so a paid run always needs a click.
 */
export function useKeptLogSearch(request: LogSearchRequest): LastLogSearch | undefined {
  const { data } = useQuery<LastLogSearch | null>({
    queryKey: LAST_KEY,
    queryFn: () => null,
    enabled: false,
    staleTime: Infinity,
    gcTime: Infinity,
  })
  return data && JSON.stringify(data.request) === JSON.stringify(request) ? data : undefined
}

/**
 * The scan estimate for a request. It reads only log group sizes on the server (no query, no cost), so it
 * runs as the form changes; `enabled` keeps it off while the form is incomplete. The query text does not
 * change the bound, so it is left out of the request and of the cache key.
 */
export function useLogSearchEstimate(request: LogSearchRequest, enabled: boolean) {
  const body: LogSearchRequest = { ...request, query: '' }
  return useQuery({
    queryKey: ['log-search', 'estimate', body],
    queryFn: ({ signal }) => apiFetch<LogSearchEstimateDTO>('/api/log-search/estimate', { method: 'POST', body: JSON.stringify(body), signal }),
    enabled,
    staleTime: SERVICES_STALE_MS,
    retry: false,
  })
}

export function useSavedLogSearches() {
  return useQuery({
    queryKey: SAVED_KEY,
    queryFn: () => api.get<ListDTO<SavedLogSearchDTO>>('/api/log-search/saved'),
  })
}

export function useSaveLogSearch() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: SaveLogSearchBody) => api.post<SavedLogSearchDTO>('/api/log-search/saved', body),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: SAVED_KEY }),
  })
}

export function useDeleteSavedLogSearch() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.del(`/api/log-search/saved/${encodeURIComponent(id)}`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: SAVED_KEY }),
  })
}
