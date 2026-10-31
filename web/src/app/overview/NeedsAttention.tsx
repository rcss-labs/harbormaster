import type { ReactNode } from 'react'
import { Link } from 'react-router'
import { useAlerts } from '../../api/observe'
import type { AlertDTO } from '../../api/types'
import { Ago } from '../../components/Ago'
import { Card } from '../../components/Card'
import { EmptyState } from '../../components/EmptyState'
import { Pill, type PillKind } from '../../components/Pill'
import { useAuth } from '../auth/AuthProvider'
import { useFilters } from '../shell/useFilters'
import styles from './overview.module.css'

const SEVERITY: Record<AlertDTO['severity'], { rank: number; kind: PillKind; label: string; stripe: string }> = {
  danger: { rank: 0, kind: 'bad', label: 'Failing', stripe: styles.stripeBad },
  warning: { rank: 1, kind: 'warn', label: 'Warning', stripe: styles.stripeWarn },
  info: { rank: 2, kind: 'info', label: 'In progress', stripe: styles.stripeInfo },
}

/** Where an alert row leads: its service tab, the accounts page for account alerts (admins only), else nowhere. */
function alertTarget(alert: AlertDTO, isAdmin: boolean): string | null {
  if (alert.service_id !== '') return `/services/${alert.service_id}/${alert.tab || 'overview'}`
  return isAdmin ? '/admin/accounts' : null
}

export function NeedsAttention() {
  const { filters } = useFilters()
  const { me } = useAuth()
  const alerts = useAlerts(filters).data
  const items = [...(alerts?.items ?? [])].sort((a, b) => SEVERITY[a.severity].rank - SEVERITY[b.severity].rank)

  return (
    <Card title="Needs attention" right={`${items.length} open`}>
      {items.length === 0 ? (
        <EmptyState>Nothing needs attention.</EmptyState>
      ) : (
        items.map((alert, index) => {
          const severity = SEVERITY[alert.severity]
          const target = alertTarget(alert, me?.admin ?? false)
          const className = `${styles.alertRow} ${severity.stripe}`
          const detail = alert.cluster === '' ? alert.text : `${alert.cluster} · ${alert.text}`
          const content: ReactNode = (
            <>
              <Pill kind={severity.kind}>{severity.label}</Pill>
              <div className={styles.what}>
                <strong>{alert.title}</strong>
                <span className={styles.mutedSmall}>{detail}</span>
              </div>
              <span className={styles.when}>
                <Ago ts={alert.at} />
              </span>
            </>
          )
          return target === null ? (
            <div key={index} className={className} data-testid="alert-row">
              {content}
            </div>
          ) : (
            <Link key={index} to={target} className={className} data-testid="alert-row">
              {content}
            </Link>
          )
        })
      )}
    </Card>
  )
}
