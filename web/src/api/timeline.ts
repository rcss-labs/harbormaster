// Types and hooks for the Service timeline endpoints. They mirror internal/api/timeline.go.
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { api } from './client'
import { queryString } from './hooks'
import type { TaskDTO } from './types'

export type TimelineRange = '1h' | '6h' | '24h' | '7d'
export type TimelineLane = 'change' | 'stop' | 'alert' | 'logs' | 'access'
export type TimelineSeverity = 'change' | 'bad' | 'warn' | 'info'

export interface TimelineDiffRow {
  label: string
  from: string
  to: string
}

export interface TimelineEvent {
  id: string
  lane: TimelineLane
  severity: TimelineSeverity
  title: string
  actor: string
  detail: string
  op: string
  task_id: string
  at: string
  /** Set only on an alert whose time was clamped to the range start. */
  evidence_at: string | null
  diff: TimelineDiffRow[]
  from_revision: number | null
  to_revision: number | null
}

export interface TimelineLaneInfo {
  lane: TimelineLane
  truncated: boolean
  note: string
}

export interface TimelineIncident {
  first_failure_at: string
  last_failure_at: string
  failure_count: number
  top_reason: string
  top_reason_count: number
  /** Reads were cut off before the burst's start or trigger: the count is a lower bound and no trigger is offered. */
  trigger_unknown: boolean
  trigger: TimelineEvent | null
  rollback_revision: number | null
  rolled_back: TimelineEvent | null
}

export interface TimelineDTO {
  range: TimelineRange
  from: string
  to: string
  events: TimelineEvent[]
  lanes: TimelineLaneInfo[]
  incident: TimelineIncident | null
  deployed_revision: number
  polled_at: string
}

export interface TimelineLogBucket {
  start: string
  /** A lower bound when capped; meaningless when not known. */
  count: number
  capped: boolean
  known: boolean
  top_message: string
}

export interface TimelineLogErrorsDTO {
  state: 'ok' | 'partial' | 'unavailable' | 'no_logs'
  message: string
  bucket_seconds: number
  buckets: TimelineLogBucket[]
  fetched_at: string | null
}

const TIMELINE_REFETCH_MS = 15_000
const LOG_ERRORS_REFETCH_MS = 60_000

// The query keys start with ['service', id] so invalidating the service after a change refreshes the timeline.
export function useTimeline(serviceId: string, range: TimelineRange) {
  return useQuery({
    queryKey: ['service', serviceId, 'timeline', range],
    queryFn: () => api.get<TimelineDTO>(`/api/services/${serviceId}/timeline${queryString({ range })}`),
    placeholderData: keepPreviousData,
    refetchInterval: TIMELINE_REFETCH_MS,
    refetchIntervalInBackground: false,
  })
}

/** Log error counts cost CloudWatch calls, so they load only with the Logs permission and the lane on. */
export function useTimelineLogErrors(serviceId: string, range: TimelineRange, enabled: boolean) {
  return useQuery({
    queryKey: ['service', serviceId, 'timeline', 'log-errors', range],
    queryFn: () => api.get<TimelineLogErrorsDTO>(`/api/services/${serviceId}/timeline/log-errors${queryString({ range })}`),
    enabled,
    refetchInterval: LOG_ERRORS_REFETCH_MS,
    refetchIntervalInBackground: false,
  })
}

/** One task, including a stopped one the Tasks list no longer shows; pass null to stay idle. */
export function useTimelineTask(serviceId: string, taskId: string | null) {
  return useQuery({
    queryKey: ['service', serviceId, 'timeline', 'task', taskId],
    queryFn: () => api.get<TaskDTO>(`/api/services/${serviceId}/timeline/tasks/${taskId}`),
    enabled: taskId !== null,
  })
}
