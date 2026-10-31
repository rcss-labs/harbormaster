// TanStack hooks for the observe endpoints. Metrics refresh once a minute (CloudWatch is slow and billed per call);
// snapshot hooks (target health, scaling, alerts) refresh every 5 s like the other live data. Neither runs in a background tab.
import { useQuery } from '@tanstack/react-query'
import { ApiError, api } from './client'
import { queryString, type Filters } from './hooks'
import type {
  AlertsDTO,
  ClusterMetricsDTO,
  ClustersMetricsDTO,
  LogsHistoryDTO,
  OverviewMetricsDTO,
  Range,
  ScalingDTO,
  Series,
  ServiceMetricsDTO,
  TargetHealthDTO,
} from './types'

const METRICS_REFETCH_MS = 60_000
const SNAPSHOT_REFETCH_MS = 5_000

const metricsPolling = { refetchInterval: METRICS_REFETCH_MS, refetchIntervalInBackground: false } as const
const snapshotPolling = { refetchInterval: SNAPSHOT_REFETCH_MS, refetchIntervalInBackground: false } as const

export function useServiceMetrics(id: string, range: Range) {
  return useQuery({
    queryKey: ['service', id, 'metrics', range],
    queryFn: () => api.get<ServiceMetricsDTO>(`/api/services/${id}/metrics${queryString({ range })}`),
    ...metricsPolling,
  })
}

export function useClusterMetrics(id: string, range: Range) {
  return useQuery({
    queryKey: ['cluster', id, 'metrics', range],
    queryFn: () => api.get<ClusterMetricsDTO>(`/api/clusters/${id}/metrics${queryString({ range })}`),
    ...metricsPolling,
  })
}

export function useClustersMetrics(filters: Filters = {}) {
  return useQuery({
    queryKey: ['clusters', filters, 'metrics'],
    queryFn: () => api.get<ClustersMetricsDTO>(`/api/clusters/metrics${queryString({ ...filters })}`),
    ...metricsPolling,
  })
}

export function useOverviewMetrics(filters: Filters = {}) {
  return useQuery({
    queryKey: ['overview', filters, 'metrics'],
    queryFn: () => api.get<OverviewMetricsDTO>(`/api/overview/metrics${queryString({ ...filters })}`),
    ...metricsPolling,
  })
}

export function useTargetHealth(id: string) {
  return useQuery({
    queryKey: ['service', id, 'target-health'],
    queryFn: () => api.get<TargetHealthDTO>(`/api/services/${id}/target-health`),
    ...snapshotPolling,
  })
}

export function useScaling(id: string) {
  return useQuery({
    queryKey: ['service', id, 'scaling'],
    queryFn: () => api.get<ScalingDTO>(`/api/services/${id}/scaling`),
    ...snapshotPolling,
  })
}

export function useAlerts(filters: Filters = {}) {
  return useQuery({
    queryKey: ['alerts', filters],
    queryFn: () => api.get<AlertsDTO>(`/api/alerts${queryString({ ...filters })}`),
    ...snapshotPolling,
  })
}

export interface LogHistoryParams {
  container?: string
  q?: string
  /** RFC 3339 cursor: only lines older than this (the previous page's `older_before`). */
  before?: string
}

/** One page of CloudWatch Logs history. Pages are immutable, so they never refetch. */
export function useLogHistory(id: string, { container, q, before }: LogHistoryParams = {}) {
  return useQuery({
    queryKey: ['service', id, 'logs', container, q, before],
    queryFn: () => api.get<LogsHistoryDTO>(`/api/services/${id}/logs${queryString({ container, q, before })}`),
    staleTime: Infinity,
  })
}

/** True when every point of every series is null (or there are no points): nothing to draw. */
export function allNull(...series: Series[]): boolean {
  return series.every((s) => s.every((v) => v === null))
}

export const isThrottled = (err: unknown): boolean => err instanceof ApiError && err.code === 'throttled'
export const isAwsError = (err: unknown): boolean => err instanceof ApiError && err.code === 'aws_error'
