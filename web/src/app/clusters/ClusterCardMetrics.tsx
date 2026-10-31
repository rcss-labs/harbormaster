import type { ClustersMetricsDTO, Series } from '../../api/types'
import { pct } from '../../charts/format'
import { Sparkline } from '../../charts/Sparkline'
import { Banner } from '../../components/Banner'
import styles from './clusters.module.css'

export type ClusterMetricsItem = ClustersMetricsDTO['items'][string]

export interface ClusterCardMetricsProps {
  clusterId: string
  insights: boolean
  /** Undefined while the clusters metrics are still loading. */
  metrics?: ClusterMetricsItem
}

/** A whole-number percentage, or a dash when the value is missing. */
export const formatPercent = (value: number | null | undefined): string => (value == null ? '–' : `${pct(value, 100)}%`)

/** Sparklines draw numbers only, so missing datapoints are dropped. */
export const presentPoints = (series: Series): number[] => series.filter((value): value is number => value !== null)

/** CPU and memory sparklines for a cluster card; replaces the card's Insights-off banner so there is one owner. */
export function ClusterCardMetrics({ clusterId, insights, metrics }: ClusterCardMetricsProps) {
  if (!insights) {
    return <Banner kind="warn">Container Insights is off, so there are no CPU and memory graphs.</Banner>
  }

  const cpuPoints = presentPoints(metrics?.cpu ?? [])
  const memoryPoints = presentPoints(metrics?.mem ?? [])

  return (
    <div className={styles.mini2} data-cluster-id={clusterId}>
      <div>
        <div className={styles.miniK}>
          <span>CPU, 24 h</span>
          <b>{formatPercent(metrics?.cpu_now)}</b>
        </div>
        {cpuPoints.length > 0 && <Sparkline values={cpuPoints} color="var(--series-1)" height={30} fluid />}
      </div>
      <div>
        <div className={styles.miniK}>
          <span>Memory, 24 h</span>
          <b>{formatPercent(metrics?.mem_now)}</b>
        </div>
        {memoryPoints.length > 0 && <Sparkline values={memoryPoints} color="var(--series-2)" height={30} fluid />}
      </div>
    </div>
  )
}
