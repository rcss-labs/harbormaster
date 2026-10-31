import { Link } from 'react-router'
import type { ServiceDTO } from '../../api/types'
import { Pill } from '../../components/Pill'
import { statusKind, statusLabel } from '../../components/status'
import { TagChip } from '../../components/TagChip'
import type { TagClickHandler } from '../clusters/tagTarget'
import { HeaderActions } from './HeaderActions'
import styles from './service.module.css'

interface ServiceHeaderProps {
  service: ServiceDTO
  /** Opens the tag dialog; passed only for admins. */
  onTagClick?: TagClickHandler
}

export function ServiceHeader({ service, onTagClick }: ServiceHeaderProps) {
  const launchType = service.launch_type === 'FARGATE' ? 'Fargate' : service.launch_type
  return (
    <div className={styles.pageHead}>
      <div className={styles.titles}>
        <div className={styles.titleRow}>
          <h1>{service.name}</h1>
          <TagChip
            tag={service.tag}
            kind="service"
            inheritedFrom={service.tag.inherited ? service.cluster.name : undefined}
            onClick={onTagClick && (() => onTagClick({ kind: 'service', id: service.id, name: service.name, tag: service.tag }))}
          />
          <Pill kind={statusKind(service.status)}>{statusLabel(service.status)}</Pill>
        </div>
        <div className={styles.meta}>
          <Link className={styles.accentLink} to={`/clusters/${service.cluster.id}`}>
            {service.cluster.name}
          </Link>
          <span>{service.account_name}</span>
          <span>{service.region}</span>
          <span className={styles.mono}>
            {service.family}:{service.revision}
          </span>
          <span>{launchType}</span>
        </div>
      </div>
      <div className={styles.actions}>
        <HeaderActions serviceId={service.id} service={service} />
      </div>
    </div>
  )
}
