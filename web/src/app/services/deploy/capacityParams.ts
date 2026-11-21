const BASE_MAX = 1000

export const CAPACITY_PARAMS = ['capacity', 'on_demand_base'] as const

/** The Deploy tab link of a Fargate Spot suggestion: the strategy itself is fixed (rest on Spot), only the on-demand base is a number. */
export function spotHref(serviceId: string, onDemandBase: number): string {
  const query = new URLSearchParams({ capacity: 'spot', on_demand_base: String(onDemandBase) })
  return `/services/${encodeURIComponent(serviceId)}/deploy?${query}`
}

/** The suggestion in the URL, or null unless it is exactly `capacity=spot` with a whole-number base of 0 to 1000. */
export function parseCapacityParams(params: URLSearchParams): { onDemandBase: number } | null {
  const base = params.get('on_demand_base')
  if (params.get('capacity') !== 'spot' || base === null || !/^\d+$/.test(base)) return null
  const onDemandBase = Number(base)
  return onDemandBase <= BASE_MAX ? { onDemandBase } : null
}

/** Why Spot cannot be chosen: Harbormaster never changes a cluster's capacity providers, it only says how. */
export const clusterNoSpotMessage = (cluster: string): string =>
  `Cluster ${cluster} has no FARGATE_SPOT capacity provider. Associate it with the cluster (in Terraform: aws_ecs_cluster_capacity_providers), then this service qualifies.`

export const spotReason = (onDemandBase: number): string => `Move to Fargate Spot, ${onDemandBase} on-demand`
