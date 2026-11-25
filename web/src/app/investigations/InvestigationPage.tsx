import { Link, useParams } from 'react-router'
import { ApiError } from '../../api/client'
import { useIncidentInvestigation, useInvestigation } from '../../api/investigations'
import { EmptyState } from '../../components/EmptyState'
import { Panel } from '../../components/Panel'
import { useAuth } from '../auth/AuthProvider'
import { InvestigationError } from './InvestigationError'
import { InvestigationView } from './InvestigationView'

/** `/investigations/:id` and `/investigations/incident/:incidentId`: where notifications and the admin list lead. */
export function InvestigationPage({ byIncident = false }: { byIncident?: boolean }) {
  const { id = '', incidentId = '' } = useParams()
  const { me } = useAuth()
  const numericId = /^[0-9]{1,9}$/.test(id) ? Number(id) : null
  const byId = useInvestigation(byIncident ? null : numericId)
  const byIncidentQuery = useIncidentInvestigation(incidentId, byIncident)
  const query = byIncident ? byIncidentQuery : byId

  if (!me?.agent_enabled || (!byIncident && numericId === null)) return <p>Page not found.</p>
  const notFound = query.error instanceof ApiError && query.error.status === 404
  if (notFound && !byIncident) return <p>Page not found.</p>
  if (notFound) {
    return (
      <Panel title="Investigation">
        <EmptyState>No investigation yet for this alert.</EmptyState>
        <p>
          <Link to="/services">Open services</Link>
        </p>
      </Panel>
    )
  }
  return (
    <Panel title="Investigation" loading={query.isPending}>
      {query.data ? <InvestigationView investigation={query.data} /> : <InvestigationError error={query.error} onRetry={() => void query.refetch()} />}
    </Panel>
  )
}
