import { useQuery, type UseQueryOptions } from '@tanstack/react-query'
import { api } from './client'
import type {
  AccountsDTO,
  AuditDTO,
  ClusterCardDTO,
  ClusterDTO,
  ListDTO,
  MeDTO,
  OperationsDTO,
  OverviewDTO,
  PermissionsDTO,
  PollerDTO,
  SearchDTO,
  ServiceDTO,
  TagAssignmentsDTO,
  TagsDTO,
  TasksFilter,
  TasksListDTO,
  UsersDTO,
} from './types'

export interface Filters {
  account?: string
  region?: string
}

export interface TasksParams extends Filters {
  filter?: TasksFilter
  q?: string
  cursor?: string
  limit?: number
}

/** Builds `?a=b&c=d`, leaving out unset values. */
export function queryString(params: Record<string, string | number | boolean | undefined | null>): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== null && value !== '') search.set(key, String(value))
  }
  const text = search.toString()
  return text ? `?${text}` : ''
}

// Live data refreshes every 5 s, but not while the tab is in the background.
const live = { refetchInterval: 5000, refetchIntervalInBackground: false } as const

type Options<T> = Partial<Pick<UseQueryOptions<T>, 'enabled'>>

export function useMe() {
  return useQuery({ queryKey: ['me'], queryFn: () => api.get<MeDTO>('/api/me'), retry: false })
}

export function useOverview(filters: Filters = {}) {
  return useQuery({
    queryKey: ['overview', filters],
    queryFn: () => api.get<OverviewDTO>(`/api/overview${queryString({ ...filters })}`),
    ...live,
  })
}

export function useClusters(filters: Filters = {}) {
  return useQuery({
    queryKey: ['clusters', filters],
    queryFn: () => api.get<ListDTO<ClusterCardDTO>>(`/api/clusters${queryString({ ...filters })}`),
    ...live,
  })
}

export function useCluster(id: string) {
  return useQuery({
    queryKey: ['cluster', id],
    queryFn: () => api.get<ClusterDTO>(`/api/clusters/${id}`),
    ...live,
  })
}

export function useService(id: string) {
  return useQuery({
    queryKey: ['service', id],
    queryFn: () => api.get<ServiceDTO>(`/api/services/${id}`),
    ...live,
  })
}

export function useServiceTasks(id: string, cursor?: string) {
  return useQuery({
    queryKey: ['service', id, 'tasks', cursor],
    queryFn: () => api.get<TasksListDTO>(`/api/services/${id}/tasks${queryString({ cursor })}`),
    ...live,
  })
}

export function useTasks(params: TasksParams = {}) {
  return useQuery({
    queryKey: ['tasks', params],
    queryFn: () => api.get<TasksListDTO>(`/api/tasks${queryString({ ...params })}`),
    ...live,
  })
}

export function useSearch(q: string) {
  return useQuery({
    queryKey: ['search', q],
    queryFn: () => api.get<SearchDTO>(`/api/search${queryString({ q })}`),
    enabled: q.length >= 2,
  })
}

export function useOperations(id: string, includeEcs: boolean, cursor?: string) {
  return useQuery({
    queryKey: ['operations', id, includeEcs, cursor],
    queryFn: () =>
      api.get<OperationsDTO>(`/api/services/${id}/operations${queryString({ include_ecs: includeEcs, cursor })}`),
    ...live,
  })
}

export function usePollerStatus() {
  return useQuery({ queryKey: ['poller'], queryFn: () => api.get<PollerDTO>('/api/poller'), ...live })
}

// ---- Admin ----

export function useUsers(options: Options<UsersDTO> = {}) {
  return useQuery({ queryKey: ['users'], queryFn: () => api.get<UsersDTO>('/api/users'), ...options })
}

export function usePermissions(options: Options<PermissionsDTO> = {}) {
  return useQuery({ queryKey: ['permissions'], queryFn: () => api.get<PermissionsDTO>('/api/permissions'), ...options })
}

export function useTags(options: Options<TagsDTO> = {}) {
  return useQuery({ queryKey: ['tags'], queryFn: () => api.get<TagsDTO>('/api/tags'), ...options })
}

export function useTagAssignments(filters: Filters = {}, options: Options<TagAssignmentsDTO> = {}) {
  return useQuery({
    queryKey: ['tag-assignments', filters],
    queryFn: () => api.get<TagAssignmentsDTO>(`/api/tag-assignments${queryString({ ...filters })}`),
    ...options,
  })
}

export function useAccounts(options: Options<AccountsDTO> = {}) {
  return useQuery({ queryKey: ['accounts'], queryFn: () => api.get<AccountsDTO>('/api/accounts'), ...options })
}

export function useAudit(cursor?: string) {
  return useQuery({
    queryKey: ['audit', cursor],
    queryFn: () => api.get<AuditDTO>(`/api/audit${queryString({ cursor })}`),
  })
}
