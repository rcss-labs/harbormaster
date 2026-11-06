import type { APIRequestContext } from '@playwright/test'

export interface Targets {
  clusterId: string
  serviceId: string
  /** Short id of a task that belongs to no service (the seeded release-build run), or null when none is listed. */
  standaloneTaskShortId: string | null
  /** True when GET /api/services answers 200 (lane 0B adds it; the SPA /services page comes with lane 1D). */
  hasServicesApi: boolean
}

interface ClusterCard {
  id: string
  name: string
  services: { id: string; name: string }[]
}

let cached: Promise<Targets> | undefined

/** Resolves ids from the live API once per worker; works with or without GET /api/services. */
export function resolveTargets(request: APIRequestContext): Promise<Targets> {
  cached ??= load(request)
  return cached
}

async function load(request: APIRequestContext): Promise<Targets> {
  const clusters = ((await (await request.get('/api/clusters')).json()).items ?? []) as ClusterCard[]
  const cluster = clusters.find((c) => c.name === 'core-services') ?? clusters.find((c) => c.services.length > 0)
  if (!cluster) throw new Error('no cluster with services; run make floci-seed and the setup project first')
  const service = cluster.services.find((s) => s.name === 'payments-api') ?? cluster.services[0]!

  const servicesResponse = await request.get('/api/services')
  const hasServicesApi = servicesResponse.ok()

  const tasks = ((await (await request.get('/api/tasks')).json()).items ?? []) as { short_id: string; service: unknown }[]
  const standalone = tasks.find((task) => task.service === null)
  return { clusterId: cluster.id, serviceId: service.id, standaloneTaskShortId: standalone?.short_id ?? null, hasServicesApi }
}
