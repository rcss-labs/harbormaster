import { Link } from 'react-router'
import type { StorageItem } from '../../api/storage'
import { Pill } from '../../components/Pill'
import { Sparkline } from '../../charts/Sparkline'
import { formatMiBps, formatSize } from './format'
import styles from './storage.module.css'

interface EfsCardProps {
  fs: StorageItem
  /** Colour of the size sparkline; matches the file system's bar in the stored-data chart. */
  colour: string
}

const clientLabel = (count: number) => `${count} ${count === 1 ? 'client' : 'clients'}`

/** Growth rounded to a whole percent with its sign, e.g. "+29% in 30 days" or "-3% in 30 days". */
const growthLabel = (pct: number) => {
  const rounded = Math.round(pct)
  return `${rounded >= 0 ? '+' : ''}${rounded}% in 30 days`
}

/** One EFS file system: identity, 30-day size and 24-hour throughput, access points and the services that mount it. */
export function EfsCard({ fs, colour }: EfsCardProps) {
  const identity = [fs.file_system_id, fs.region, fs.vpc?.name].filter(Boolean).join(' · ')
  const clients = fs.client_connections ?? fs.mounting_tasks
  // CloudWatch returning no datapoints is treated like a failed call: both mean there is nothing to draw.
  const sizeSeries = fs.size_30d?.length ? fs.size_30d.map((point) => point.v) : null
  const throughputSeries = fs.throughput_24h?.length ? fs.throughput_24h.map((point) => point.v) : null
  const latestThroughput = throughputSeries?.at(-1)
  const metricsMissing = sizeSeries === null && throughputSeries === null

  return (
    <section className={styles.xcard} aria-label={fs.name}>
      <div className={styles.top}>
        <div>
          <h3 className={styles.name}>{fs.name}</h3>
          <div className={`${styles.mono} ${styles.muted}`}>{identity}</div>
        </div>
        {fs.mounted ? <Pill kind="ok">{clientLabel(clients)}</Pill> : <Pill kind="warn">Not mounted</Pill>}
      </div>

      <div className={styles.mini2}>
        <div>
          <div className={styles.miniKey}>
            <span>Size, 30 days</span>
            <b>{formatSize(fs.size_bytes)}</b>
          </div>
          {sizeSeries && <Sparkline values={sizeSeries} color={colour} height={28} fluid />}
          {fs.growth_30d_pct !== null && <div className={`${styles.muted} ${styles.small}`}>{growthLabel(fs.growth_30d_pct)}</div>}
        </div>
        <div>
          <div className={styles.miniKey}>
            <span>Throughput, 24 h</span>
            {latestThroughput !== undefined && <b>{formatMiBps(latestThroughput)}</b>}
          </div>
          {throughputSeries && <Sparkline values={throughputSeries} color="var(--series-4)" height={28} fluid />}
          <div className={`${styles.muted} ${styles.small}`}>{fs.throughput_mode}</div>
        </div>
      </div>
      {metricsMissing && <div className={`${styles.muted} ${styles.small}`}>Metrics unavailable</div>}

      <div>
        <div className={styles.miniKey}>Access points</div>
        {fs.access_points.length === 0 ? (
          <span className={`${styles.muted} ${styles.small}`}>None</span>
        ) : (
          fs.access_points.map((accessPoint) => (
            <div key={accessPoint.access_point_id} className={`${styles.mono} ${styles.small}`}>
              {`${accessPoint.access_point_id} · ${accessPoint.path} · uid ${accessPoint.uid ?? '—'}`}
            </div>
          ))
        )}
      </div>

      <div>
        <div className={styles.miniKey}>Mounted by</div>
        {fs.mounted_by.length === 0 ? (
          <span className={`${styles.warn} ${styles.small}`}>No service mounts this file system.</span>
        ) : (
          fs.mounted_by.map((mount, index) => (
            <div key={index} className={styles.small}>
              <Link to={`/services/${mount.service_id}/overview`} className={styles.link}>{mount.service}</Link>{' '}
              <span className={styles.muted}>{`(${mount.cluster}) →`}</span>{' '}
              <span className={styles.mono}>{mount.container_path}</span>
            </div>
          ))
        )}
      </div>
    </section>
  )
}
