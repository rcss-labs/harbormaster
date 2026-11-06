import { useSearchParams } from 'react-router'
import { useService } from '../../../api/hooks'
import { allNull, useServiceMetrics } from '../../../api/observe'
import type { Range, Series } from '../../../api/types'
import { Legend } from '../../../charts/Legend'
import { LineChart } from '../../../charts/LineChart'
import { Banner } from '../../../components/Banner'
import { Panel } from '../../../components/Panel'
import { Seg } from '../../../components/Seg'
import { queryPanelError } from '../panelError'
import { fmtNum, niceMax } from '../../../charts/format'
import { percentText } from '../../clusters/ServiceCardMetrics'
import { MetricPanel } from '../../observe/MetricPanel'
import styles from '../service.module.css'

const RANGES: Range[] = ['1h', '6h', '24h', '72h']
const DEFAULT_RANGE: Range = '24h'
const CHART_HEIGHT = 150
const NETWORK_MIN_MAX = 10
const TASK_COUNT_MIN_MAX = 4
const CPU_COLOR = 'var(--series-1)'
const MEMORY_COLOR = 'var(--series-2)'
const REQUESTS_COLOR = 'var(--series-3)'
const P95_COLOR = 'var(--series-4)'

/** CloudWatch period in seconds to the label shown in the toolbar. */
const PERIOD_LABELS: Record<number, string | undefined> = { 60: '1-minute', 300: '5-minute', 1200: '20-minute', 3600: '1-hour' }

const X_LABELS: Record<Range, string[]> = {
  '1h': ['1 h ago', '30 min', 'now'],
  '6h': ['6 h ago', '3 h', 'now'],
  '24h': ['24 h ago', '12 h', 'now'],
  '72h': ['72 h ago', '36 h', 'now'],
}

const isRange = (value: string | null): value is Range => RANGES.some((range) => range === value)

/** Largest datapoint across the series, 0 when there are none. */
const peakOf = (...series: Series[]): number => Math.max(0, ...series.flat().filter((value): value is number => value !== null))

/** Service metrics from Container Insights: CPU, memory, network and running task count over the chosen range. */
export function MetricsTab({ serviceId }: { serviceId: string }) {
  const [searchParams, setSearchParams] = useSearchParams()
  const requestedRange = searchParams.get('range')
  const range: Range = isRange(requestedRange) ? requestedRange : DEFAULT_RANGE
  const service = useService(serviceId).data
  const metricsQuery = useServiceMetrics(serviceId, range)
  const metrics = metricsQuery.data

  if (!service) return null

  if (metricsQuery.isError && !metrics) {
    return (
      <Panel title="Metrics" error={queryPanelError(metricsQuery, 'Metrics could not be read.')}>
        {null}
      </Panel>
    )
  }

  if (metrics?.insights === 'disabled') {
    return (
      <Banner kind="warn">
        {`Container Insights is off for ${service.cluster.name}, so per-service metrics are not available. Turn it on in the cluster's Terraform (`}
        <span className={styles.mono}>{'setting { name = "containerInsights" value = "enhanced" }'}</span>
        {').'}
      </Banner>
    )
  }

  const selectRange = (value: Range) =>
    setSearchParams((previous) => {
      const next = new URLSearchParams(previous)
      next.set('range', value)
      return next
    })

  const cpu = metrics?.series?.cpu ?? []
  const memory = metrics?.series?.mem ?? []
  const received = metrics?.series?.net_rx_kbps ?? []
  const sent = metrics?.series?.net_tx_kbps ?? []
  const tasks = metrics?.series?.tasks ?? []
  const alb = service.networking.load_balancers.length > 0 ? metrics?.alb : null
  const requestMax = alb ? niceMax(Math.max(0, ...alb.req_per_min.filter((value): value is number => value !== null))) : 0
  const p95Max = alb ? niceMax(Math.max(0, ...alb.p95_ms.filter((value): value is number => value !== null))) : 0
  const networkMax = Math.max(NETWORK_MIN_MAX, Math.ceil(peakOf(received, sent) / 10) * 10)
  const taskCountMax = Math.max(TASK_COUNT_MIN_MAX, service.desired + 1)
  const resolution = metrics ? PERIOD_LABELS[metrics.period] ?? `${metrics.period}-second` : ''
  const xLabels = X_LABELS[range]

  return (
    <>
      <div className={styles.metricsToolbar}>
        <Seg label="Range" options={RANGES.map((value) => ({ value, label: value }))} value={range} onChange={selectRange} />
        {metrics && (
          <span className={`${styles.muted} ${styles.small}`}>
            {`Container Insights via CloudWatch · ${resolution} resolution, averaged across ${service.running} tasks`}
          </span>
        )}
      </div>
      {!metrics && (
        <Panel title="Metrics" loading>
          {null}
        </Panel>
      )}
      {metrics && (
        <div className={styles.metricsGrid}>
          <MetricPanel
            title="CPU utilization"
            right={`now ${percentText(metrics.now?.cpu)} of ${metrics.now?.cpu_units ?? '–'} units`}
            stale={metrics.stale}
            empty={allNull(cpu)}
          >
            <LineChart series={[{ name: 'CPU', color: CPU_COLOR, values: cpu }]} xLabels={xLabels} height={CHART_HEIGHT} />
          </MetricPanel>
          <MetricPanel
            title="Memory utilization"
            right={`now ${percentText(metrics.now?.mem)} of ${metrics.now?.mem_mib ?? '–'} MiB`}
            stale={metrics.stale}
            empty={allNull(memory)}
          >
            <LineChart series={[{ name: 'Memory', color: MEMORY_COLOR, values: memory }]} xLabels={xLabels} height={CHART_HEIGHT} />
          </MetricPanel>
          {alb && (
            <>
              <MetricPanel
                title="Requests per minute"
                right={alb.req_now === null ? undefined : `now ${fmtNum(alb.req_now)}`}
                stale={metrics.stale}
                empty={allNull(alb.req_per_min)}
              >
                <LineChart series={[{ name: 'Requests', color: REQUESTS_COLOR, values: alb.req_per_min }]} unit="" max={requestMax} xLabels={xLabels} height={CHART_HEIGHT} />
              </MetricPanel>
              <MetricPanel
                title="Response time p95"
                right={alb.p95_now === null ? undefined : `now ${Math.round(alb.p95_now)} ms`}
                stale={metrics.stale}
                empty={allNull(alb.p95_ms)}
              >
                <LineChart series={[{ name: 'p95', color: P95_COLOR, values: alb.p95_ms }]} unit=" ms" max={p95Max} xLabels={xLabels} height={CHART_HEIGHT} />
              </MetricPanel>
            </>
          )}
          <MetricPanel
            title="Network"
            right={
              <Legend
                items={[
                  { name: 'Received', color: CPU_COLOR },
                  { name: 'Sent', color: MEMORY_COLOR },
                ]}
              />
            }
            stale={metrics.stale}
            empty={allNull(received, sent)}
          >
            <LineChart
              series={[
                { name: 'Received', color: CPU_COLOR, values: received },
                { name: 'Sent', color: MEMORY_COLOR, values: sent },
              ]}
              unit=" KB/s"
              max={networkMax}
              xLabels={xLabels}
              height={CHART_HEIGHT}
            />
          </MetricPanel>
          <MetricPanel title="Running task count" right={`desired ${service.desired}`} stale={metrics.stale} empty={allNull(tasks)}>
            <LineChart
              series={[{ name: 'Running tasks', color: CPU_COLOR, values: tasks }]}
              unit=""
              max={taskCountMax}
              xLabels={xLabels}
              height={CHART_HEIGHT}
            />
          </MetricPanel>
        </div>
      )}
    </>
  )
}
