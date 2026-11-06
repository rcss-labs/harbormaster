import { Link } from 'react-router'
import type { ServiceListItemDTO, ServicesMetricsDTO } from '../../../api/types'
import { Ago } from '../../../components/Ago'
import { Banner } from '../../../components/Banner'
import { Drawer } from '../../../components/Drawer'
import { KV } from '../../../components/KV'
import { StatusDot } from '../../../components/StatusDot'
import { TagChip } from '../../../components/TagChip'
import { useNarrow } from '../../../components/useNarrow'
import { restartBlockedReason } from './filterServices'
import { metricFigure, type MetricKind } from './serviceMetrics'
import styles from './services.module.css'

/** The six places a service can be opened at; History is the `operations` tab. */
const OPEN_LINKS: [label: string, tab: string][] = [
  ['Overview', 'overview'],
  ['Tasks', 'tasks'],
  ['Logs', 'logs'],
  ['Metrics', 'metrics'],
  ['Deploy', 'deploy'],
  ['History', 'operations'],
]

interface ServiceDrawerProps {
  service: ServiceListItemDTO | undefined
  onClose: () => void
  onRestart: (service: ServiceListItemDTO) => void
  metrics: ServicesMetricsDTO | undefined
  metricsFailed: boolean
}

/** Side panel with one service's facts; opening it changes nothing on the page behind it. */
export function ServiceDrawer({ service, onClose, onRestart, metrics, metricsFailed }: ServiceDrawerProps) {
  const phone = useNarrow()
  if (!service) return null
  const figure = (kind: MetricKind) => {
    const { text, title, tone } = metricFigure(service, metrics, metricsFailed, kind)
    return (
      <span className={`${styles.mono} ${tone === 'warn' ? styles.warnText : ''}`.trim()} title={title}>
        {text}
      </span>
    )
  }
  const blocked = restartBlockedReason(service)
  return (
    <Drawer
      open
      onClose={onClose}
      title={<span className={styles.mono}>{service.name}</span>}
      sub={<StatusDot status={service.status} />}
      footer={
        <div className={styles.footBtns}>
          <button type="button" className={styles.btn} onClick={onClose}>
            Close
          </button>
          <button
            type="button"
            className={`${styles.btn} ${styles.btnPrimary}`}
            title={blocked ?? `Restart ${service.name}`}
            disabled={blocked !== undefined}
            onClick={() => onRestart(service)}
          >
            Restart
          </button>
        </div>
      }
    >
      <div className={styles.drawerBody}>
        {service.attention && <Banner kind="warn">{service.attention}</Banner>}
        {phone && blocked && <p className={styles.why}>{`Restart: ${blocked}`}</p>}
        <KV
          rows={[
            ['Cluster', <Link key="cluster" to={`/clusters/${service.cluster.id}`} className={`${styles.mono} ${styles.link}`}>{service.cluster.name}</Link>],
            ['Account / region', `${service.account_name} · ${service.region}`],
            [
              'Tag',
              <span key="tag">
                <TagChip tag={{ ...service.tag, inherited: service.tag_inherited }} inheritedFrom={service.tag_inherited ? service.cluster.name : undefined} />
                <span className={styles.tagNote}>{service.tag_inherited ? 'Inherited from the cluster' : 'Set on this service'}</span>
              </span>,
            ],
            ['Tasks', <span key="tasks" className={styles.mono}>{service.running} running of {service.desired} desired</span>],
            ['Revision', <span key="rev" className={styles.mono}>:{service.revision}</span>],
            ['Image tag', <span key="image" className={styles.mono}>{service.image_tag}</span>],
            ['CPU', figure('cpu')],
            ['Memory', figure('mem')],
            ['Last change', service.last_change_at ? <Ago key="last" ts={service.last_change_at} /> : '–'],
          ]}
        />
        <div className={styles.openGroup} role="group" aria-label="Open">
          <span className={styles.openLabel} aria-hidden="true">
            Open
          </span>
          <div className={styles.openLinks}>
            {OPEN_LINKS.map(([label, tab]) => (
              <Link key={tab} className={styles.btn} to={`/services/${service.id}/${tab}`}>
                {label}
              </Link>
            ))}
          </div>
        </div>
      </div>
    </Drawer>
  )
}
