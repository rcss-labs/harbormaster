// Types and hook for GET /api/config-search. They mirror the Go DTOs (JSON is snake_case). Only names are ever
// returned: environment values and secret references never leave the server.
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { api } from './client'
import { queryString, type Filters } from './hooks'
import type { ClusterRef, TagDTO } from './types'

export interface ConfigMatchDTO {
  field: string
  /** Empty for the two role fields. */
  container: string
  value: string
}

export interface ConfigContainerDTO {
  name: string
  image: string
  tag: string
  /** The repository's configured base image; empty for images outside ECR or with no base set. */
  base_image: string
  log_group: string
  ports: number[]
  env_names: string[]
  secret_names: string[]
}

export interface ConfigSearchItemDTO {
  id: string
  name: string
  cluster: ClusterRef
  account_id: string
  account_name: string
  region: string
  tag: TagDTO
  tag_inherited: boolean
  family: string
  /** The PRIMARY revision. */
  revision: number
  /** The revision that matched; differs from `revision` when `previous` is true. */
  matched_revision: number
  previous: boolean
  task_role: string
  execution_role: string
  containers: ConfigContainerDTO[]
  matches: ConfigMatchDTO[]
}

export interface ConfigSearchResponse {
  /** The normalized query text. */
  query: string
  items: ConfigSearchItemDTO[]
  total: number
  truncated: boolean
  searched: number
  unindexed: number
  polled_at: string
}

/** What a later "Bulk changes" feature takes as input: the query, the shell filters and the chosen service ids. */
export interface ConfigSearchSelection {
  query: string
  filters: Filters
  serviceIds: string[]
}

/** Runs only for a non-empty query; the result is stable for 30 s and never polled. */
export function useConfigSearch(q: string, filters: Filters = {}) {
  return useQuery({
    queryKey: ['config-search', q, filters],
    queryFn: () => api.get<ConfigSearchResponse>(`/api/config-search${queryString({ q, ...filters })}`),
    enabled: q.trim() !== '',
    staleTime: 30_000,
    placeholderData: keepPreviousData,
  })
}
