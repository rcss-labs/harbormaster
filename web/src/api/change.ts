// Hooks for the task-definition change path: reads poll like the rest of the app, writes invalidate the service they touch.

import { useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { api, ApiError } from './client'
import type {
  ImageTagsResponse,
  RevisionsResponse,
  TagDTO,
  TaskDefinitionResponse,
  DesiredCountResponse as DesiredCountInfo,
} from './types'

export type {
  TaskDefinitionResponse,
  RevisionsResponse,
  ImageTagsResponse,
  DesiredCountResponse as DesiredCountInfo,
  RevisionDTO as RevisionItem,
} from './types'

// Same cadence as the live hooks in hooks.ts: refresh every 5 s, but not while the tab is in the background.
const live = { refetchInterval: 5000, refetchIntervalInBackground: false } as const

// ---- Write payloads and results ----

export interface RegisterRevisionBody {
  base_revision: number
  definition: Record<string, unknown>
}

/** 201 from POST /api/services/{id}/revisions. */
export interface RegisterResult {
  revision: number
  arn: string
  image_tag: string
  cpu: string
  memory: string
  can_deploy: boolean
  tag: TagDTO
  strategy: string
}

export interface DeployBody {
  revision: number
}

/** 202 from POST /api/services/{id}/deploy. */
export interface DeployStarted {
  deployment_id: string
  from_revision: number
  to_revision: number
  kind: 'deploy'
}

/** 202 from POST /api/services/{id}/restart. */
export interface RestartStarted {
  deployment_id: string
  revision: number
  kind: 'restart'
}

export type RolloutStarted = DeployStarted | RestartStarted

export interface DesiredCountBody {
  desired: number
}

/** 200 from POST /api/services/{id}/desired-count. */
export interface DesiredCountResult {
  desired: number
  previous: number
  changed: boolean
  warning: string | null
}

/** 200 from POST /api/tasks/{id}/stop. */
export interface StopTaskResult {
  task_arn: string
  reason: string
}

// ---- Reads ----

export function useTaskDefinition(serviceId: string) {
  return useQuery({
    queryKey: ['service', serviceId, 'task-definition'],
    queryFn: () => api.get<TaskDefinitionResponse>(`/api/services/${serviceId}/task-definition`),
    ...live,
  })
}

export function useRevisions(serviceId: string) {
  return useQuery({
    queryKey: ['service', serviceId, 'revisions'],
    queryFn: () => api.get<RevisionsResponse>(`/api/services/${serviceId}/revisions`),
    ...live,
  })
}

/** Image tags are only needed when the picker is open, so the caller decides when to fetch. */
export function useImageTags(serviceId: string, enabled: boolean) {
  return useQuery({
    queryKey: ['service', serviceId, 'image-tags'],
    queryFn: () => api.get<ImageTagsResponse>(`/api/services/${serviceId}/image-tags`),
    staleTime: 60_000,
    enabled,
  })
}

export function useDesiredCountInfo(serviceId: string) {
  return useQuery({
    queryKey: ['service', serviceId, 'desired-count'],
    queryFn: () => api.get<DesiredCountInfo>(`/api/services/${serviceId}/desired-count`),
  })
}

// ---- Writes ----

function invalidateService(queryClient: QueryClient, serviceId: string) {
  return queryClient.invalidateQueries({ queryKey: ['service', serviceId] })
}

export function useRegisterRevision(serviceId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: RegisterRevisionBody) =>
      api.post<RegisterResult>(`/api/services/${serviceId}/revisions`, body),
    onSuccess: () => invalidateService(queryClient, serviceId),
  })
}

export function useDeploy(serviceId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: DeployBody) => api.post<DeployStarted>(`/api/services/${serviceId}/deploy`, body),
    onSuccess: () => invalidateService(queryClient, serviceId),
  })
}

export function useRestart(serviceId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => api.post<RestartStarted>(`/api/services/${serviceId}/restart`),
    onSuccess: () => invalidateService(queryClient, serviceId),
  })
}

export function useSetDesiredCount(serviceId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: DesiredCountBody) =>
      api.post<DesiredCountResult>(`/api/services/${serviceId}/desired-count`, body),
    onSuccess: () => invalidateService(queryClient, serviceId),
  })
}

/**
 * Stopping a task changes the task lists. Pass the service id when the caller is on a service page
 * so that service's Tasks tab refreshes too; the global list is always refreshed.
 */
export function useStopTask(serviceId?: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (taskId: string) => api.post<StopTaskResult>(`/api/tasks/${taskId}/stop`),
    onSuccess: () => {
      if (serviceId) queryClient.invalidateQueries({ queryKey: ['service', serviceId, 'tasks'] })
      queryClient.invalidateQueries({ queryKey: ['tasks'] })
    },
  })
}

// ---- Errors ----

/** Narrows an unknown thrown value to ApiError, optionally to one error code (e.g. `conflict`, `invalid`). */
export function isApiError(error: unknown, code?: string): error is ApiError {
  return error instanceof ApiError && (code === undefined || error.code === code)
}
