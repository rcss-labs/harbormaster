// Wire types for GET /api/analytics/{delivery,reliability,capacity,operations,cost} and the hook that fetches them.
// The types mirror internal/api/analytics_dto.go.
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useFilters } from '../app/shell/useFilters'
import { api } from './client'
import { queryString } from './hooks'
import type { TagDTO } from './types'

const METRICS_REFETCH_MS = 60_000
const metricsPolling = { refetchInterval: METRICS_REFETCH_MS, refetchIntervalInBackground: false } as const

export type AnalyticsTab = 'delivery' | 'reliability' | 'capacity' | 'operations' | 'cost'
export type AnalyticsRange = '24h' | '7d' | '30d' | '90d'

export interface AnalyticsWindow {
  requested: string
  used: string
  clamped: boolean
  /** False on capacity, which always reads the latest poll plus 7 days of counts history. */
  range_applies: boolean
  bucket: '1h' | '4h' | '1d' | '2d'
  from: string
  to: string
  /** Governing table's retention such as "168h"; empty when kept forever. */
  retention: string
}

export interface AnalyticsSource {
  table: string
  reads: string
  kept: string
  setting: string | null
  /** Set when this table's window differs from the tab's, e.g. "30d". */
  window_used: string | null
}

export interface SeriesLine {
  key: string
  label: string
  /** Grey styling for expected events. */
  expected: boolean
  /** null means no sample; counts are zero-filled instead. */
  values: (number | null)[]
}

export interface AnalyticsSeries {
  timestamps: string[]
  series: SeriesLine[]
}

export interface DonutSlice {
  key: string
  label: string
  value: number
}

export interface AnalyticsDonut {
  total: number
  slices: DonutSlice[]
}

/** A panel read from a daily rollup table (or the recorded failure reasons): `data` is null until `available`. */
export interface RollupPanel<T> {
  available: boolean
  /** Earliest day in the rollup table once it has data. */
  data_since: string | null
  data: T | null
}

/** At most 50 rows; `total` is the size of the matching population. */
export interface TopTable<R> {
  rows: R[]
  total: number
}

interface Envelope<Tab extends AnalyticsTab> {
  tab: Tab
  window: AnalyticsWindow
  sources: AnalyticsSource[]
  polled_at: string
}

// ---- delivery ----

export interface DeliveryKpis {
  rollouts: number
  rollouts_per_day: number
  /** 0..1; null with zero rollouts. */
  change_failure_rate: number | null
  median_rollout_s: number | null
  p90_rollout_s: number | null
  time_to_recover_s: number | null
  rollbacks: number
  superseded: number
}

export interface DeliveryServiceRow {
  service_id: string
  service: string
  cluster: string
  account_id: string
  tag: TagDTO
  rollouts: number
  failed: number
  failure_rate: number
  median_s: number | null
  recover_s: number | null
  last_failed_at: string | null
}

export interface FailedRolloutRow {
  at: string
  service_id: string
  service: string
  cluster: string
  from_revision: number | null
  to_revision: number
  reason: string
  detail: string
  ran_for_s: number | null
}

export interface AnalyticsDelivery extends Envelope<'delivery'> {
  kpis: DeliveryKpis
  rollouts: AnalyticsSeries
  failure_rate: AnalyticsSeries
  duration: AnalyticsSeries
  services: TopTable<DeliveryServiceRow>
  failure_reasons: RollupPanel<AnalyticsSeries>
  recent_failures: RollupPanel<TopTable<FailedRolloutRow>>
}

// ---- reliability ----

export interface ReliabilityKpis {
  /** 0..1; null without samples. */
  availability: number | null
  minutes_below_desired: number
  services_below: number
  task_stops: number
  unexpected_stops: number
  out_of_memory: number
  placement_failures: number
}

export interface ReliabilityServiceRow {
  service_id: string
  service: string
  cluster: string
  tag: TagDTO
  availability: number
  minutes_below: number
  unexpected_stops: number
  top_reason: string | null
}

export interface AvailabilityTrendRow {
  service_id: string
  service: string
  cluster: string
  tag: TagDTO
  /** Availability of the 30 days before the last 30, as a 0..1 fraction. */
  previous: number
  /** Availability of the last 30 days, as a 0..1 fraction. */
  current: number
  /** current - previous in percentage points (negative: getting worse), e.g. -1.25. */
  change_pts: number
}

export interface AnalyticsReliability extends Envelope<'reliability'> {
  kpis: ReliabilityKpis
  availability: AnalyticsSeries
  services_below: AnalyticsSeries
  stops_by_reason: AnalyticsSeries
  unexpected_by_zone: AnalyticsSeries
  exit_codes: AnalyticsSeries
  least_available: TopTable<ReliabilityServiceRow>
  availability_90d: RollupPanel<AnalyticsSeries>
  below_desired_90d: RollupPanel<AnalyticsSeries>
  getting_worse: RollupPanel<TopTable<AvailabilityTrendRow>>
}

// ---- capacity ----

export interface CapacityKpis {
  reserved_vcpu: number
  reserved_mem_gib: number
  running_tasks: number
  services: number
  /** 0..1; null without running tasks. */
  spot_share: number | null
  at_scaling_max: number
  subnets_low_ips: number
}

export interface CapacityClusterRow {
  cluster_id: string
  cluster: string
  account_id: string
  account_name: string
  tag: TagDTO
  tasks: number
  vcpu: number
  mem_gib: number
  share: number
  spot_share: number
}

export interface AtMaxRow {
  service_id: string
  service: string
  cluster: string
  desired: number
  max_capacity: number
  /** Null when counts history cannot say. */
  at_max_since: string | null
}

export interface SubnetRow {
  subnet_id: string
  name: string
  az: string
  account_id: string
  region: string
  available_ips: number
  total_ips: number
  used_pct: number // fraction 0..1 of usable addresses in use (not a 0-100 percent)
}

export interface OverReservedRow {
  service_id: string
  service: string
  cluster: string
  tag: TagDTO
  reserved_vcpu: number
  used_p95_vcpu: number
  reserved_mem_gib: number
  used_p95_mem_gib: number
  wasted_vcpu: number
}

export interface AnalyticsCapacity extends Envelope<'capacity'> {
  kpis: CapacityKpis
  reserved_vcpu_7d: AnalyticsSeries
  vcpu_by_account: AnalyticsDonut
  tasks_by_capacity_provider: AnalyticsDonut
  clusters: TopTable<CapacityClusterRow>
  at_scaling_max: AtMaxRow[]
  subnets_low: SubnetRow[]
  reserved_vs_used_90d: RollupPanel<AnalyticsSeries>
  spot_share_90d: RollupPanel<AnalyticsSeries>
  memory_90d: RollupPanel<AnalyticsSeries>
  over_reserved: RollupPanel<TopTable<OverReservedRow>>
}

// ---- operations ----

export interface OperationsKpis {
  operations: number
  people: number
  revisions_registered: number
  scale_changes: number
  restarts: number
  tasks_stopped: number
  terminal_sessions: number
}

export interface OperatorRow {
  username: string
  total: number
  deploy: number
  restart: number
  scale: number
  terminal: number
  last_at: string
}

export interface RestartedServiceRow {
  service_id: string
  service: string
  cluster: string
  tag: TagDTO
  restarts: number
  people: number
  last_at: string
}

export interface AnalyticsOperations extends Envelope<'operations'> {
  kpis: OperationsKpis
  by_type: AnalyticsSeries
  hands_on: AnalyticsSeries
  restarts_per_100_deploys: AnalyticsSeries
  who: TopTable<OperatorRow>
  most_restarted: TopTable<RestartedServiceRow>
}

// ---- cost ----

export interface CostKpis {
  /** USD per month at list prices; Spot is included at the on-demand list price. */
  monthly_total: number
  spot_monthly: number
  priced_tasks: number
  spot_tasks: number
  /** Running tasks with no Fargate price (EC2, external, or a region without a rate). They never show a dollar figure. */
  unpriced_tasks: number
  /** Regions that have Fargate tasks but no price. */
  unpriced_regions: string[]
}

/** Spend of one cluster, account or tag. `key` is the cluster id, account id or tag id. */
export interface CostGroup {
  key: string
  label: string
  monthly: number
  spot_monthly: number
  unpriced_tasks: number
}

export interface CostSuggestion {
  cpu_units: number
  memory_mib: number
  saving_monthly: number
  limited_by_containers: boolean
}

/** Applies from `from_headroom_pct` up to the next step; a null suggestion means no worthwhile saving there. */
export interface CostStep {
  from_headroom_pct: number
  suggestion: CostSuggestion | null
}

export type CostServiceStatus =
  | 'ok'
  | 'not_fargate'
  | 'mixed_capacity'
  | 'no_price'
  | 'not_running'
  | 'no_size'
  | 'no_data'
  | 'short_history'
  | 'stale'
  | 'rollup_off'

export interface CostService {
  service_id: string
  service: string
  cluster: string
  region: string
  architecture: string
  tag: TagDTO
  tasks: number
  spot_tasks: number
  cpu_units: number | null
  memory_mib: number | null
  /** 0..1 of the current size; may exceed 1 after a downsize. Null without data. */
  cpu_used_share: number | null
  mem_used_share: number | null
  monthly: number
  status: CostServiceStatus
  history_days: number
  auto_scaled: boolean
  recent_oom: boolean
  can_edit: boolean
  /** Never null; empty unless the status is ok and some headroom stop saves money. */
  steps: CostStep[]
}

export interface CostRightsizing {
  state: 'ok' | 'rollup_off' | 'retention_short'
  min_history_days: number
  default_headroom_pct: number
  /** Every visible service, not capped: the KPIs sum them. */
  services: CostService[]
}

export interface AnalyticsCost extends Envelope<'cost'> {
  /** List price publication date, YYYY-MM-DD. */
  prices_as_of: string
  kpis: CostKpis
  by_cluster: CostGroup[]
  by_account: CostGroup[]
  by_tag: CostGroup[]
  rightsizing: CostRightsizing
}

export type AnalyticsResponse = AnalyticsDelivery | AnalyticsReliability | AnalyticsCapacity | AnalyticsOperations | AnalyticsCost

export interface AnalyticsByTab {
  delivery: AnalyticsDelivery
  reliability: AnalyticsReliability
  capacity: AnalyticsCapacity
  operations: AnalyticsOperations
  cost: AnalyticsCost
}

/**
 * One analytics tab for the current account/region/tag filters. Refreshes once a minute (the data moves at the
 * 5-minute poll cadence) and keeps the previous tab or range on screen while the next one loads.
 */
export function useAnalytics<Tab extends AnalyticsTab>(tab: Tab, range: AnalyticsRange) {
  const { filters } = useFilters()
  return useQuery({
    queryKey: ['analytics', tab, range, filters],
    queryFn: () => api.get<AnalyticsByTab[Tab]>(`/api/analytics/${tab}${queryString({ ...filters, range })}`),
    // Only carry data over within the same tab, so a tab never renders another tab's shape.
    placeholderData: (previous, previousQuery) => (previousQuery?.queryKey[1] === tab ? keepPreviousData(previous) : undefined),
    ...metricsPolling,
  })
}
