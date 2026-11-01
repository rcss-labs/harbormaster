// TanStack hook and wire types for the storage (EFS) endpoint. CloudWatch metrics are slow and billed per call,
// so the page refreshes once a minute and not in a background tab. Shapes mirror internal/api/storage.go.
import { useQuery } from '@tanstack/react-query'
import { useFilters } from '../app/shell/useFilters'
import { api } from './client'
import { queryString } from './hooks'

const METRICS_REFETCH_MS = 60_000
const metricsPolling = { refetchInterval: METRICS_REFETCH_MS, refetchIntervalInBackground: false } as const

export interface StoragePoint {
  t: string
  v: number
}

export interface StorageAccessPoint {
  access_point_id: string
  path: string
  uid: number | null
}

export interface StorageMount {
  service_id: string
  service: string
  cluster: string
  container_path: string
  tasks: number
}

export interface StorageItem {
  file_system_id: string
  name: string
  account_id: string
  region: string
  vpc: { vpc_id: string; name: string } | null
  size_bytes: number
  throughput_mode: string
  performance_mode: string
  mounted: boolean
  client_connections: number | null
  mounting_tasks: number
  access_points: StorageAccessPoint[]
  mounted_by: StorageMount[]
  /** Null when the CloudWatch part failed for this file system. */
  size_30d: StoragePoint[] | null
  growth_30d_pct: number | null
  /** Null when the CloudWatch part failed for this file system. */
  throughput_24h: StoragePoint[] | null
}

export interface StorageTotals {
  size_bytes: number
  mounted: number
  not_mounted: number
}

export interface StorageResponse {
  items: StorageItem[]
  totals: StorageTotals
  /** Set when the CloudWatch part failed; the file system rows are still valid. */
  metrics_error?: string
  polled_at: string
}

export function useStorage() {
  const { filters } = useFilters()
  return useQuery({
    queryKey: ['storage', filters],
    queryFn: () => api.get<StorageResponse>(`/api/storage${queryString({ ...filters })}`),
    ...metricsPolling,
  })
}
