import type { RevisionsResponse, ServiceDTO } from '../../api/types'

/** A rollout is running when ECS reports an IN_PROGRESS deployment or the revisions read names one rolling out. */
export function isRolloutRunning(service: ServiceDTO, revisions: RevisionsResponse | undefined): boolean {
  return service.deployments.some((deployment) => deployment.rollout_state === 'IN_PROGRESS') || revisions?.rolling_out_revision != null
}
