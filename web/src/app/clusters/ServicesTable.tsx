import { Link } from 'react-router'
import type { ClusterServiceDTO } from '../../api/types'
import { Sparkline } from '../../charts/Sparkline'
import { Ago } from '../../components/Ago'
import { Pill } from '../../components/Pill'
import { Table, tableClass } from '../../components/Table'
import { TagChip } from '../../components/TagChip'
import { statusKind, statusLabel } from '../../components/status'
import styles from './clusters.module.css'
import { type ClusterServiceMetrics, percentText, presentValues } from './ServiceCardMetrics'
import type { TagClickHandler } from './tagTarget'

interface ServicesTableProps {
  services: ClusterServiceDTO[]
  clusterName: string
  /** Given only for admins; makes the tag chip open the tag dialog. */
  onTagClick?: TagClickHandler
  /** Per-service cluster metrics keyed by service id; undefined while loading. */
  metrics?: Record<string, ClusterServiceMetrics>
  insights?: boolean
}

export function ServicesTable({ services, clusterName, onTagClick, metrics, insights = false }: ServicesTableProps) {
  return (
    <div className={styles.panel}>
      <Table>
        <thead>
          <tr>
            <th>Service</th>
            <th>Label</th>
            <th>Status</th>
            <th>Task definition</th>
            <th>Image tag</th>
            <th className={tableClass.num}>Tasks</th>
            <th>CPU, 24 h</th>
            <th className={tableClass.num}>Memory</th>
            <th>Last change</th>
          </tr>
        </thead>
        <tbody>
          {services.map((service) => {
            const serviceMetrics = metrics?.[service.id]
            const hasMetrics = insights && serviceMetrics !== undefined
            const cpuHistory = hasMetrics ? presentValues(serviceMetrics.cpu) : []
            return (
              <tr key={service.id}>
                <td className={tableClass.name}>
                  <Link to={`/services/${service.id}/overview`}>{service.name}</Link>
                </td>
                <td>
                  <TagChip
                    tag={service.tag}
                    kind="service"
                    inheritedFrom={service.tag.inherited ? clusterName : undefined}
                    onClick={onTagClick && (() => onTagClick({ kind: 'service', id: service.id, name: service.name, tag: service.tag }))}
                  />
                </td>
                <td>
                  <Pill kind={statusKind(service.status)}>{statusLabel(service.status)}</Pill>
                </td>
                <td className={styles.mono}>
                  {service.family}:{service.revision}
                </td>
                <td className={styles.mono}>{service.image_tag}</td>
                <td className={tableClass.num}>
                  {service.running} / {service.desired}
                </td>
                <td>{cpuHistory.length > 0 ? <Sparkline values={cpuHistory} height={24} /> : '–'}</td>
                <td className={tableClass.num}>{hasMetrics ? percentText(serviceMetrics.mem_now) : '–'}</td>
                <td className={styles.muted}>
                  {service.last_change_at ? <Ago ts={service.last_change_at} /> : '–'}
                </td>
              </tr>
            )
          })}
        </tbody>
      </Table>
    </div>
  )
}
