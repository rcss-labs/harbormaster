import { useMutation, useQueryClient, useQuery, type UseQueryOptions } from '@tanstack/react-query'
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
  SettingsDTO,
  TagAssignmentsDTO,
  TagsDTO,
  TasksFilter,
  TasksListDTO,
  UsersDTO,
} from './types'

export interface Filters {
  account?: string
  region?: string
  /** Tag ids; sent as a repeated ?tag= */
  tag?: number[]
}

export interface TasksParams extends Filters {
  filter?: TasksFilter
  q?: string
  cursor?: string
  limit?: number
}

/** Builds `?a=b&c=d`, leaving out unset values. Arrays repeat the key once per element (scalars come first). */
export function queryString(
  params: Record<string, string | number | boolean | undefined | null | readonly (string | number)[]>,
): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (Array.isArray(value)) continue
    if (value !== undefined && value !== null && value !== '') search.set(key, String(value))
  }
  for (const [key, value] of Object.entries(params)) {
    if (Array.isArray(value)) for (const item of value) search.append(key, String(item))
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

/** The service Tasks tab shows running and stopped tasks, so it asks for `all` (the server defaults to `problems`). */
export function useServiceTasks(id: string, cursor?: string) {
  return useQuery({
    queryKey: ['service', id, 'tasks', cursor],
    queryFn: () => api.get<TasksListDTO>(`/api/services/${id}/tasks${queryString({ filter: 'all', cursor })}`),
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

export function useSearch(q: string, filters: Filters = {}) {
  return useQuery({
    queryKey: ['search', q, filters],
    queryFn: () => api.get<SearchDTO>(`/api/search${queryString({ q, ...filters })}`),
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

export function useAudit(cursor?: string, options: Options<AuditDTO> = {}) {
  return useQuery({
    queryKey: ['audit', cursor],
    queryFn: () => api.get<AuditDTO>(`/api/audit${queryString({ cursor })}`),
    ...options,
  })
}

// ---- Settings ----

export function useSettings() {
  return useQuery({ queryKey: ['settings'], queryFn: () => api.get<SettingsDTO>('/api/settings') })
}

/** Saving the window changes which stopped tasks show, so task and service queries refetch too. */
export function useUpdateSettings() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (stoppedTasksWindowMinutes: number) =>
      api.put<SettingsDTO>('/api/settings', { stopped_tasks_window_minutes: stoppedTasksWindowMinutes }),
    onSuccess: () => {
      for (const key of ['settings', 'tasks', 'service']) void queryClient.invalidateQueries({ queryKey: [key] })
    },
  })
}
