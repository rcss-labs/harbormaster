// Hooks for the task-definition change path: reads poll like the rest of the app, writes invalidate the service they touch.

import { useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { api, ApiError } from './client'
import type {
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
  warning: string | null
}

export interface DeployBody {
  revision: number
  /** Skips the rollout-running gate for a rollback (the progress panel's one-click rollback). */
  interrupt?: boolean
}

/** 202 from POST /api/services/{id}/deploy. */
export interface DeployStarted {
  deployment_id: string
  from_revision: number
  to_revision: number
  kind: 'deploy' | 'rollback'
  warning?: string | null
}

/** 202 from POST /api/services/{id}/restart. */
export interface RestartStarted {
  deployment_id: string
  revision: number
  kind: 'restart'
  warning?: string | null
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
  warning?: string | null
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

// The server's snapshot only reflects an ECS change after its next poll (about 5 s), and the page refetches every 5 s.
// Two timers of the same period drift in and out of step: a refetch that lands just before the poll leaves the old value
// on screen for another whole period (about 10 s in all). So after a write, refetch every SETTLE_STEP_MS for
// SETTLE_WINDOW_MS, which bounds the wait to the poll plus one step.
const SETTLE_STEP_MS = 2_000
const SETTLE_WINDOW_MS = 10_000

/** Runs refetch now and then every SETTLE_STEP_MS until SETTLE_WINDOW_MS has passed. Returns the immediate refetch. */
export function refetchWhileSettling(refetch: () => Promise<void>): Promise<void> {
  for (let elapsed = SETTLE_STEP_MS; elapsed <= SETTLE_WINDOW_MS; elapsed += SETTLE_STEP_MS) {
    setTimeout(() => void refetch(), elapsed)
  }
  return refetch()
}

/** Rollouts and scaling also add entries to the operations history. */
function invalidateServiceAndOperations(queryClient: QueryClient, serviceId: string) {
  void queryClient.invalidateQueries({ queryKey: ['operations', serviceId] })
  return invalidateService(queryClient, serviceId)
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
    onSuccess: () => invalidateServiceAndOperations(queryClient, serviceId),
  })
}

export function useRestart(serviceId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => api.post<RestartStarted>(`/api/services/${serviceId}/restart`),
    onSuccess: () => invalidateServiceAndOperations(queryClient, serviceId),
  })
}

export function useSetDesiredCount(serviceId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: DesiredCountBody) =>
      api.post<DesiredCountResult>(`/api/services/${serviceId}/desired-count`, body),
    // The count changes in AWS at once but the stored service only on the next poll, so keep refetching until it does.
    onSuccess: () => refetchWhileSettling(() => invalidateServiceAndOperations(queryClient, serviceId)),
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
    onSuccess: () =>
      refetchWhileSettling(async () => {
        if (serviceId) {
          void queryClient.invalidateQueries({ queryKey: ['service', serviceId, 'tasks'] })
          void queryClient.invalidateQueries({ queryKey: ['service', serviceId] })
        }
        await queryClient.invalidateQueries({ queryKey: ['tasks'] })
      }),
  })
}

// ---- Errors ----

/** Narrows an unknown thrown value to ApiError, optionally to one error code (e.g. `conflict`, `invalid`). */
export function isApiError(error: unknown, code?: string): error is ApiError {
  return error instanceof ApiError && (code === undefined || error.code === code)
}
