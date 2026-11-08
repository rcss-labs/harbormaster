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

export type LogSearchTargetState = 'ok' | 'no_permission' | 'throttled' | 'timeout' | 'cancelled' | 'error'

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
  cost_usd: number
  duration_ms: number
  from: string
  to: string
  services: LogSearchServiceResultDTO[]
  targets: LogSearchTargetDTO[]
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
  const controller = useRef<AbortController | null>(null)
  const mutation = useMutation({
    mutationKey: ['log-search', 'run'],
    mutationFn: (body: LogSearchRequest) => {
      controller.current?.abort()
      const own = new AbortController()
      controller.current = own
      return apiFetch<LogSearchResultDTO>('/api/log-search', { method: 'POST', body: JSON.stringify(body), signal: own.signal })
    },
  })
  useEffect(() => () => controller.current?.abort(), [])
  return { ...mutation, cancel: () => controller.current?.abort() }
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
