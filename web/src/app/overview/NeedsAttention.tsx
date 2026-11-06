import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { useServicesList } from '../../api/hooks'
import { useAlerts } from '../../api/observe'
import type { AlertDTO, ServiceListItemDTO } from '../../api/types'
import { Ago } from '../../components/Ago'
import { Chips } from '../../components/Chips'
import { Icon } from '../../components/Icon'
import { usePhone } from '../../components/useNarrow'
import { EmptyState } from '../../components/EmptyState'
import { Panel } from '../../components/Panel'
import { SegmentedFilter } from '../../components/Seg'
import { statusLabel } from '../../components/status'
import { StatusDot } from '../../components/StatusDot'
import { needsAttention } from '../services/list/filterServices'
import { useAuth } from '../auth/AuthProvider'
import { useFilters } from '../shell/useFilters'
import { ActionsSheet, type ActionsTarget } from './ActionsSheet'
import { RestartButton } from './RestartButton'
import { useSeverityFilter, type SeverityFilter } from './useOverviewState'
import styles from './overview.module.css'

const ROW_CAP = 6

type RowSeverity = Exclude<SeverityFilter, 'all'>

const SEVERITY: Record<AlertDTO['severity'], { key: RowSeverity; kind: 'bad' | 'warn' | 'info'; label: string }> = {
  danger: { key: 'failing', kind: 'bad', label: 'Failing' },
  warning: { key: 'warning', kind: 'warn', label: 'Warning' },
  info: { key: 'progress', kind: 'info', label: 'In progress' },
}

/** Where an alert row leads: its service tab, the accounts page for account alerts (admins only), else nowhere. */
function alertTarget(alert: AlertDTO, isAdmin: boolean): string | null {
  if (alert.service_id !== '') return `/services/${alert.service_id}/${alert.tab || 'overview'}`
  return isAdmin ? '/settings/accounts' : null
}

/** One row per service: the API sorts worst first, so the first alert of a service is its worst. */
function onePerService(alerts: AlertDTO[]): AlertDTO[] {
  const seen = new Set<string>()
  return alerts.filter((alert) => {
    if (alert.service_id === '') return true
    if (seen.has(alert.service_id)) return false
    seen.add(alert.service_id)
    return true
  })
}

const STATUS_SEVERITY: Partial<Record<ServiceListItemDTO['status'], AlertDTO['severity']>> = {
  failing: 'danger',
  degraded: 'warning',
  unhealthy_task: 'warning',
  deploying: 'info',
}
const SEVERITY_RANK: Record<AlertDTO['severity'], number> = {
  danger: 0,
  warning: 1,
  info: 2,
}

/**
 * A row for a service that needs attention by status but has no alert yet (the degraded alert waits for three polls).
 * It states only what the list already shows; no cause, next step or "just before" is made up.
 */
function statusRow(service: ServiceListItemDTO, at: string): AlertDTO {
  const text = `${statusLabel(service.status)}: ${service.running} of ${service.desired} tasks running.`
  return {
    kind: `status_${service.status}`,
    severity: STATUS_SEVERITY[service.status] ?? 'warning',
    title: `${service.name}: ${service.running} of ${service.desired} tasks running`,
    text,
    cause: text,
    at: service.last_change_at ?? at,
    service_id: service.id,
    service: service.name,
    cluster: service.cluster.name,
    cluster_id: service.cluster.id,
    tab: 'overview',
    next_step: { label: '', href: '' },
    just_before: null,
  }
}

export function NeedsAttention() {
  const { filters } = useFilters()
  const { me } = useAuth()
  const alerts = useAlerts(filters).data
  const services = useServicesList(filters).data
  const [severity, setSeverity] = useSeverityFilter()
  const [showAll, setShowAll] = useState(false)
  const phone = usePhone()
  const [sheet, setSheet] = useState<ActionsTarget | null>(null)

  const rows = useMemo(() => {
    const alerted = onePerService(alerts?.items ?? [])
    const withAlert = new Set(alerted.map((alert) => alert.service_id))
    const statusOnly = (services?.items ?? [])
      .filter((service) => needsAttention(service) && !withAlert.has(service.id))
      .map((service) => statusRow(service, services?.polled_at ?? ''))
    // Stable sort keeps the API's worst-first order within a severity.
    return [...alerted, ...statusOnly].sort((a, b) => SEVERITY_RANK[a.severity] - SEVERITY_RANK[b.severity])
  }, [alerts, services])
  const accountAlerts = rows.filter((alert) => alert.service_id === '').length
  const serviceCount = rows.length - accountAlerts
  const count = (key: RowSeverity) => rows.filter((alert) => SEVERITY[alert.severity].key === key).length
  const matching = severity === 'all' ? rows : rows.filter((alert) => SEVERITY[alert.severity].key === severity)
  const visible = showAll ? matching : matching.slice(0, ROW_CAP)

  const severityOptions = [
    { value: 'all' as const, label: 'All', count: rows.length },
    { value: 'failing' as const, label: 'Failing', count: count('failing') },
    { value: 'warning' as const, label: 'Warning', count: count('warning') },
    {
      value: 'progress' as const,
      label: 'In progress',
      count: count('progress'),
    },
  ]
  const changeSeverity = (next: SeverityFilter) => {
    setSeverity(next)
    setShowAll(false)
  }
  const toolbar = (
    <div className={styles.severity}>
      {phone ? (
        <Chips label="Severity" value={severity} onChange={changeSeverity} options={severityOptions} />
      ) : (
        <SegmentedFilter label="Severity" value={severity} onChange={changeSeverity} options={severityOptions} />
      )}
    </div>
  )

  return (
    <Panel
      title="Needs attention"
      toolbar={toolbar}
      actions={
        <span>
          {`${serviceCount} ${serviceCount === 1 ? 'service' : 'services'}`}
          {accountAlerts > 0 && ` · ${accountAlerts} account ${accountAlerts === 1 ? 'alert' : 'alerts'}`}
        </span>
      }
      flush
    >
      <div className={styles.attention}>
        {matching.length === 0 ? (
          <EmptyState>Nothing needs attention.</EmptyState>
        ) : (
          visible.map((alert) => {
            const level = SEVERITY[alert.severity]
            const target = alertTarget(alert, me?.admin ?? false)
            const hasService = alert.service_id !== ''
            // The next step comes only from the API; account rows have their own Open accounts action.
            const hasNextStep = hasService && alert.next_step.href !== ''
            const rowKey = `${alert.kind}-${alert.service_id}-${alert.at}`
            if (phone) {
              const subject = alert.service || alert.title
              return (
                <div key={rowKey} className="hm-lr" data-hm-row data-status={level.kind} data-testid="alert-row">
                  <div className={`hm-lr-main ${styles.phoneRow}`}>
                    <div className="hm-l1">
                      <StatusDot kind={level.kind}>{level.label}</StatusDot>
                      <span className="hm-l2" style={{ margin: 0 }}>
                        <Ago ts={alert.at} />
                      </span>
                    </div>
                    {target === null ? (
                      <strong className={styles.whatTitle}>{alert.title}</strong>
                    ) : (
                      <Link to={target} className={styles.whatTitle} data-hm-rl>
                        {alert.title}
                      </Link>
                    )}
                    {alert.cluster !== '' && <span className={styles.where}>{alert.cluster}</span>}
                    <span className={styles.cause}>{alert.cause || alert.text}</span>
                    {hasNextStep && (
                      <span className={styles.context}>
                        Next step:{' '}
                        <Link to={alert.next_step.href} className={styles.contextLink}>
                          {alert.next_step.label}
                        </Link>
                      </span>
                    )}
                    {alert.just_before && <span className={styles.context}>{`Just before: ${alert.just_before.text}`}</span>}
                  </div>
                  {(hasService || me?.admin) && (
                    <button
                      type="button"
                      className={styles.moreBtn}
                      aria-label={`Actions for ${subject}`}
                      aria-haspopup="dialog"
                      onClick={() =>
                        setSheet({
                          label: subject,
                          sub: alert.cluster || undefined,
                          serviceId: alert.service_id,
                        })
                      }
                    >
                      <Icon name="more" size={18} />
                    </button>
                  )}
                </div>
              )
            }
            return (
              <div key={rowKey} className={`${styles.alertRow} ${styles[`stripe_${level.kind}`]}`} data-testid="alert-row">
                <span className={styles.status}>
                  <StatusDot kind={level.kind}>{level.label}</StatusDot>
                </span>
                <div className={styles.what}>
                  {target === null ? (
                    <strong className={styles.whatTitle}>{alert.title}</strong>
                  ) : (
                    <Link to={target} className={styles.whatTitle}>
                      {alert.title}
                    </Link>
                  )}
                  {alert.cluster !== '' && <span className={styles.where}>{alert.cluster}</span>}
                </div>
                <div className={styles.detail}>
                  <span className={styles.cause}>{alert.cause || alert.text}</span>
                  {hasNextStep && (
                    <span className={styles.context}>
                      Next step:{' '}
                      <Link to={alert.next_step.href} className={styles.contextLink}>
                        {alert.next_step.label}
                      </Link>
                    </span>
                  )}
                  {alert.just_before && <span className={styles.context}>{`Just before: ${alert.just_before.text}`}</span>}
                </div>
                <span className={styles.when}>
                  <Ago ts={alert.at} />
                </span>
                <div className={styles.rowActions}>
                  {hasService ? (
                    <>
                      <Link to={`/services/${alert.service_id}/logs`} className={styles.rowAction}>
                        Logs
                      </Link>
                      <Link to={`/services/${alert.service_id}/tasks`} className={styles.rowAction}>
                        Tasks
                      </Link>
                      <RestartButton serviceId={alert.service_id} />
                    </>
                  ) : (
                    me?.admin && (
                      <Link to="/settings/accounts" className={styles.rowAction}>
                        Open accounts
                      </Link>
                    )
                  )}
                </div>
              </div>
            )
          })
        )}
        {matching.length > ROW_CAP && (
          <button type="button" className={styles.showAll} onClick={() => setShowAll((all) => !all)}>
            {showAll ? 'Show fewer' : `Show all ${matching.length}`}
          </button>
        )}
      </div>
      {sheet && <ActionsSheet target={sheet} isAdmin={me?.admin ?? false} onClose={() => setSheet(null)} />}
    </Panel>
  )
}
