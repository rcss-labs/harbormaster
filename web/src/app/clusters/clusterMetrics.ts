import type { ClustersMetricsDTO, Series } from '../../api/types'
import { pct } from '../../charts/format'

export type ClusterMetricsItem = ClustersMetricsDTO['items'][string]

/** A whole-number percentage, or a dash when the value is missing. */
export const formatPercent = (value: number | null | undefined): string => (value == null ? '–' : `${pct(value, 100)}%`)

/** Sparklines draw numbers only, so missing datapoints are dropped. */
export const presentPoints = (series: Series): number[] => series.filter((value): value is number => value !== null)
