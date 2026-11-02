import type { ClusterMetricsDTO } from '../../api/types'
import { pct } from '../../charts/format'

export type ClusterServiceMetrics = ClusterMetricsDTO['services'][string]

/** Drops missing datapoints; the sparkline has no gap support. */
export const presentValues = (series: (number | null)[]): number[] => series.filter((value): value is number => value !== null)

/** Percentage for display; '–' when the metric has no datapoint. */
export const percentText = (value: number | null | undefined): string =>
  value === null || value === undefined ? '–' : `${pct(value, 100)}%`
