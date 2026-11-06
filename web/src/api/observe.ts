// TanStack hooks for the observe endpoints. Metrics refresh once a minute (CloudWatch is slow and billed per call);
// snapshot hooks (target health, scaling, alerts) refresh every 5 s like the other live data. Neither runs in a background tab.
import { useQuery } from '@tanstack/react-query'
import { ApiError, api } from './client'
import { queryString, usePollerStatus, type Filters } from './hooks'
import type {
  AlertsDTO,
  ClusterMetricsDTO,
  ClustersMetricsDTO,
  LogsHistoryDTO,
  OverviewMetricsDTO,
  PollerEntryDTO,
  PollerTierDTO,
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

export interface TaskLogsOptions {
  /** Pass false until the drawer is open; nothing is requested while false. */
  enabled?: boolean
  limit?: number
  container?: string
}

/** The newest log lines of one task (`task` is the short id, 8+ chars). Standalone tasks have no service, so callers pass enabled=false. */
export function useTaskLogs(serviceId: string, taskShortId: string, { enabled = true, limit = 20, container }: TaskLogsOptions = {}) {
  return useQuery({
    queryKey: ['service', serviceId, 'logs', 'task', taskShortId, container, limit],
    queryFn: () => api.get<LogsHistoryDTO>(`/api/services/${serviceId}/logs${queryString({ task: taskShortId, limit, container })}`),
    enabled,
    refetchInterval: SNAPSHOT_REFETCH_MS,
    refetchIntervalInBackground: false,
  })
}

/** A region (account x region) with at least one failing or throttled data type the page depends on. */
export interface AffectedRegion extends PollerEntryDTO {
  failingTiers: PollerTierDTO[]
}

export interface DataProblems {
  /** Regions where a tier named in `labels` fails or is throttled. */
  affected: AffectedRegion[]
  /** Regions with no failing or throttled core tier; a non-core failure (e.g. auto scaling) does not count. */
  current: number
  total: number
  isLoading: boolean
}

/**
 * Which regions have a data problem a page cares about. `labels` are `PollerTierDTO.label` values the page depends on
 * (for example `['Auto scaling']`); omit it to consider every tier. `current`/`total` count core tiers only, so a failing
 * non-core tier never labels the whole region stale.
 */
export function useDataProblems(labels?: readonly string[]): DataProblems {
  const poller = usePollerStatus()
  const items = poller.data?.items ?? []
  const isProblem = (tier: PollerTierDTO) => tier.failing || tier.throttled
  const affected = items.flatMap((item) => {
    const failingTiers = item.tiers.filter((tier) => isProblem(tier) && (!labels || labels.includes(tier.label)))
    return failingTiers.length > 0 ? [{ ...item, failingTiers }] : []
  })
  const current = items.filter((item) => !item.tiers.some((tier) => tier.core && isProblem(tier))).length
  return { affected, current, total: items.length, isLoading: poller.isLoading }
}

/** True when every point of every series is null (or there are no points): nothing to draw. */
export function allNull(...series: Series[]): boolean {
  return series.every((s) => s.every((v) => v === null))
}

export const isThrottled = (err: unknown): boolean => err instanceof ApiError && err.code === 'throttled'
export const isAwsError = (err: unknown): boolean => err instanceof ApiError && err.code === 'aws_error'
