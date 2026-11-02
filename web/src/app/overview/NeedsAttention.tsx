import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { useAlerts } from '../../api/observe'
import type { AlertDTO } from '../../api/types'
import { Ago } from '../../components/Ago'
import { EmptyState } from '../../components/EmptyState'
import { Panel } from '../../components/Panel'
import { SegmentedFilter } from '../../components/Seg'
import { StatusDot } from '../../components/StatusDot'
import { useAuth } from '../auth/AuthProvider'
import { useFilters } from '../shell/useFilters'
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

export function NeedsAttention() {
  const { filters } = useFilters()
  const { me } = useAuth()
  const alerts = useAlerts(filters).data
  const [severity, setSeverity] = useSeverityFilter()
  const [showAll, setShowAll] = useState(false)

  const rows = useMemo(() => onePerService(alerts?.items ?? []), [alerts])
  const count = (key: RowSeverity) => rows.filter((alert) => SEVERITY[alert.severity].key === key).length
  const matching = severity === 'all' ? rows : rows.filter((alert) => SEVERITY[alert.severity].key === severity)
  const visible = showAll ? matching : matching.slice(0, ROW_CAP)

  const toolbar = (
    <SegmentedFilter
      label="Severity"
      value={severity}
      onChange={(next) => {
        setSeverity(next)
        setShowAll(false)
      }}
      options={[
        { value: 'all', label: 'All', count: rows.length },
        { value: 'failing', label: 'Failing', count: count('failing') },
        { value: 'warning', label: 'Warning', count: count('warning') },
        { value: 'progress', label: 'In progress', count: count('progress') },
      ]}
    />
  )

  return (
    <Panel title="Needs attention" toolbar={toolbar} actions={<span>{rows.length} open</span>} flush>
      <div className={styles.attention}>
        {matching.length === 0 ? (
          <EmptyState>Nothing needs attention.</EmptyState>
        ) : (
          visible.map((alert) => {
            const level = SEVERITY[alert.severity]
            const target = alertTarget(alert, me?.admin ?? false)
            const hasService = alert.service_id !== ''
            return (
              <div
                key={`${alert.kind}-${alert.service_id}-${alert.at}`}
                className={`${styles.alertRow} ${styles[`stripe_${level.kind}`]}`}
                data-testid="alert-row"
              >
                <StatusDot kind={level.kind}>{level.label}</StatusDot>
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
                <span className={styles.detail} title={alert.text}>
                  {alert.text}
                </span>
                <span className={styles.when}>
                  <Ago ts={alert.at} />
                </span>
                <div className={styles.rowActions}>
                  {hasService && (
                    <>
                      <Link to={`/services/${alert.service_id}/logs`} className={styles.rowAction}>
                        Logs
                      </Link>
                      <Link to={`/services/${alert.service_id}/tasks`} className={styles.rowAction}>
                        Tasks
                      </Link>
                      <RestartButton serviceId={alert.service_id} />
                    </>
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
    </Panel>
  )
}
