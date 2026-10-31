import { Link } from 'react-router'
import type { ClusterCardDTO } from '../../api/types'
import { Ago } from '../../components/Ago'
import { Banner } from '../../components/Banner'
import { Pill } from '../../components/Pill'
import { TagChip } from '../../components/TagChip'
import { STATUS_COLOR, clusterStatusKind, clusterStatusLabel, statusKind, statusLabel } from '../../components/status'
import { fmtNum, pct } from '../../charts/format'
import styles from './clusters.module.css'

export function ClusterCard({ cluster }: { cluster: ClusterCardDTO }) {
  const barPercent = cluster.desired ? (cluster.running / cluster.desired) * 100 : 0
  const barColor = cluster.running < cluster.desired ? 'var(--warning)' : 'var(--success)'

  return (
    <article className={styles.xcard} aria-label={cluster.name}>
      <div className={styles.xcardTop}>
        <div>
          <div className={styles.titleRow}>
            <Link to={`/clusters/${cluster.id}`} className={`${styles.big} ${styles.stretch}`}>
              {cluster.name}
            </Link>
            <TagChip tag={cluster.tag} kind="cluster" />
          </div>
          <div className={`${styles.muted} ${styles.small}`}>
            {cluster.account_name} · {cluster.region}
          </div>
        </div>
        <Pill kind={clusterStatusKind(cluster.status)}>{clusterStatusLabel(cluster.status)}</Pill>
      </div>

      {!cluster.container_insights && (
        <Banner kind="warn">Container Insights is off, so there are no CPU and memory graphs.</Banner>
      )}

      <div>
        <div className={styles.miniK}>
          Tasks <b>{cluster.running} running of {cluster.desired}</b>
        </div>
        <div className={styles.bar}>
          <span style={{ width: `${barPercent}%`, background: barColor }} />
        </div>
      </div>

      <div>
        <div className={styles.miniK}>Services</div>
        <div className={styles.chips}>
          {cluster.services.map((service) => (
            <span key={service.id} className={styles.chip} title={statusLabel(service.status)}>
              <i style={{ background: STATUS_COLOR[statusKind(service.status)] }} />
              {service.name}
            </span>
          ))}
        </div>
      </div>

      <div className={styles.footStats}>
        <span>
          <b>{fmtNum(cluster.reserved_vcpu)}</b> vCPU
        </span>
        <span>
          <b>{fmtNum(cluster.reserved_gib)}</b> GiB reserved
        </span>
        <span>
          <b>{pct(cluster.spot_tasks, cluster.running)}%</b> on Spot
        </span>
        <span>last change {cluster.last_change_at ? <Ago ts={cluster.last_change_at} /> : '–'}</span>
      </div>
    </article>
  )
}
