import type { ReactNode } from 'react'
import type { ClusterMetricsDTO, ServiceRef } from '../../api/types'
import { Sparkline } from '../../charts/Sparkline'
import { fmtNum, pct } from '../../charts/format'
import styles from './clusters.module.css'

export type ClusterServiceMetrics = ClusterMetricsDTO['services'][string]

interface ServiceCardMetricsProps {
  service: ServiceRef
  /** This service's metrics from the cluster response; undefined while loading or when absent. */
  m?: ClusterServiceMetrics
  insights: boolean
  /** Rendered last in the foot-stats row, after the load balancer stats. */
  children?: ReactNode
}

const CPU_COLOR = 'var(--series-1)'
const MEMORY_COLOR = 'var(--series-2)'

/** Drops missing datapoints; the sparkline has no gap support. */
export const presentValues = (series: (number | null)[]): number[] => series.filter((value): value is number => value !== null)

/** Percentage for display; '–' when the metric has no datapoint. */
export const percentText = (value: number | null | undefined): string =>
  value === null || value === undefined ? '–' : `${pct(value, 100)}%`

const numberText = (value: number | null, format: (value: number) => string): string => (value === null ? '–' : format(value))

/** CPU and memory sparklines (Container Insights only) and the load balancer foot stats of a service card (mockup xcard). */
export function ServiceCardMetrics({ service, m, insights, children }: ServiceCardMetricsProps) {
  return (
    <>
      {insights && m && (
        <div className={styles.mini2}>
          <div>
            <div className={styles.miniK}>
              CPU <b>{percentText(m.cpu_now)}</b>
            </div>
            <Sparkline values={presentValues(m.cpu)} color={CPU_COLOR} height={28} fluid />
          </div>
          <div>
            <div className={styles.miniK}>
              Memory <b>{percentText(m.mem_now)}</b>
            </div>
            <Sparkline values={presentValues(m.mem)} color={MEMORY_COLOR} height={28} fluid />
          </div>
        </div>
      )}
      <div className={styles.footStats} role="group" aria-label={`${service.name} metrics`}>
        {m?.alb ? (
          <>
            <span>
              <b>{numberText(m.alb.req_now, fmtNum)}</b> req/min
            </span>
            <span>
              <b>{numberText(m.alb.p95_now, (value) => String(Math.round(value)))}</b> ms p95
            </span>
            <span>
              <b>{m.alb.err5xx_today}</b> 5xx today
            </span>
          </>
        ) : (
          m && <span>No load balancer</span>
        )}
        {children}
      </div>
    </>
  )
}
