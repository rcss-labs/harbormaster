import { matchPath, useLocation } from 'react-router'
import type { AgentPageContext } from '../../api/types'
import { useCrumbsValue } from '../shell/crumbs'

/** What the user is looking at: the router path with the service or cluster id when on one, and a name to show for it. */
export function usePageContext(): { page: AgentPageContext; label: string } {
  const { pathname } = useLocation()
  const crumbs = useCrumbsValue()
  const page: AgentPageContext = { path: pathname }
  const service = matchPath('/services/:serviceId/*', pathname)?.params.serviceId
  const cluster = matchPath('/clusters/:clusterId/*', pathname)?.params.clusterId
  if (service) page.service_id = service
  if (cluster) page.cluster_id = cluster
  const label = crumbs.length > 0 ? crumbs[crumbs.length - 1][0] : ''
  return { page, label }
}
