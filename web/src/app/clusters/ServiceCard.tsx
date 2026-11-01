import { Link } from 'react-router'
import type { ClusterServiceDTO } from '../../api/types'
import { Pill } from '../../components/Pill'
import { Slots } from '../../components/Slots'
import { TagChip } from '../../components/TagChip'
import { statusKind, statusLabel } from '../../components/status'
import styles from './clusters.module.css'
import { type ClusterServiceMetrics, ServiceCardMetrics } from './ServiceCardMetrics'
import type { TagClickHandler } from './tagTarget'

interface ServiceCardProps {
  service: ClusterServiceDTO
  clusterName: string
  /** Given only for admins; makes the tag chip open the tag dialog. */
  onTagClick?: TagClickHandler
  /** This service's cluster metrics; undefined while loading. */
  metrics?: ClusterServiceMetrics
  insights?: boolean
}

export function ServiceCard({ service, clusterName, onTagClick, metrics, insights = false }: ServiceCardProps) {
  const latestNotDeployed =
    service.latest_registered_revision !== null &&
    service.latest_registered_revision > service.revision &&
    service.status !== 'deploying'
  const cpuVcpu = (Number(service.cpu) / 1024).toFixed(2)

  return (
    <article className={styles.xcard} aria-label={service.name}>
      <div className={styles.xcardTop}>
        <div>
          <div className={styles.titleRow}>
            <Link to={`/services/${service.id}/overview`} className={`${styles.big} ${styles.stretch}`}>
              {service.name}
            </Link>
            <span className={styles.above}>
              <TagChip
                tag={service.tag}
                kind="service"
                inheritedFrom={service.tag.inherited ? clusterName : undefined}
                onClick={onTagClick && (() => onTagClick({ kind: 'service', id: service.id, name: service.name, tag: service.tag }))}
              />
            </span>
          </div>
          <div className={`${styles.muted} ${styles.small} ${styles.mono}`}>
            :{service.revision} · {service.image_tag}
            {latestNotDeployed && ` · :${service.latest_registered_revision} not deployed`}
          </div>
        </div>
        <Pill kind={statusKind(service.status)}>{statusLabel(service.status)}</Pill>
      </div>

      <div>
        <div className={styles.miniK}>
          Tasks <b>{service.running} / {service.desired}</b>
        </div>
        <Slots slots={service.slots} desired={service.desired} />
      </div>

      <ServiceCardMetrics service={service} m={metrics} insights={insights}>
        <span>
          {cpuVcpu} vCPU · {service.memory} MiB
        </span>
      </ServiceCardMetrics>
    </article>
  )
}
