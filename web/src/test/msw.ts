import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import {
  ACCOUNTS,
  ASSIGNMENTS,
  AUDIT,
  CLUSTERS,
  CLUSTERS_METRICS,
  CLUSTER_DETAILS,
  CLUSTER_METRICS_CORE,
  ME_ADMIN,
  OPERATIONS_FOR,
  OVERVIEW,
  PERMISSIONS,
  POLLER,
  SERVICE_DETAILS,
  SERVICE_METRICS_CATALOG_NO_INSIGHTS,
  SERVICE_METRICS_PAYMENTS,
  SERVICE_PAYMENTS,
  TARGET_HEALTH_EDGE,
  TAGS,
  USERS,
  ALERTS,
  ALL_TASKS,
  FIXTURE_NOW,
  OVERVIEW_METRICS,
  filterTasks,
  tasksList,
  tasksResponse,
} from './fixtures'
import type { SearchResultDTO } from '../api/types'

const notFound = () => HttpResponse.json({ error: { code: 'not_found', message: 'Not found.' } }, { status: 404 })

function search(q: string): SearchResultDTO[] {
  const needle = q.toLowerCase()
  if (needle.length < 2) return []
  const results: SearchResultDTO[] = [
    ...CLUSTERS.items.map((c) => ({ kind: 'cluster' as const, label: c.name, sub: `${c.account_name} · ${c.region}`, href: `/clusters/${c.id}` })),
    ...Object.values(SERVICE_DETAILS).map((s) => ({ kind: 'service' as const, label: s.name, sub: `${s.cluster.name} · ${s.region}`, href: `/services/${s.id}/overview` })),
    // A standalone task (no service) links to its cluster.
    ...ALL_TASKS.map((t) => ({
      kind: 'task' as const,
      label: t.short_id,
      sub: t.service ? `${t.service.name} · ${t.cluster.name}` : `${t.family} · ${t.cluster.name}`,
      href: t.service ? `/services/${t.service.id}/tasks` : `/clusters/${t.cluster.id}`,
    })),
  ]
  return results.filter((r) => r.label.toLowerCase().includes(needle)).slice(0, 8)
}

/** Default handlers: every GET in the API reference answers with the fixtures. Override per test with `server.use`. */
export const handlers = [
  http.get('/api/me', () => HttpResponse.json(ME_ADMIN)),
  http.get('/api/auth/setup', () => HttpResponse.json({ needed: false })),
  http.get('/api/overview', () => HttpResponse.json(OVERVIEW)),
  http.get('/api/clusters', ({ request }) => {
    const url = new URL(request.url)
    const account = url.searchParams.get('account')
    const region = url.searchParams.get('region')
    const items = CLUSTERS.items.filter((c) => (!account || c.account_id === account) && (!region || c.region === region))
    return HttpResponse.json({ ...CLUSTERS, items })
  }),
  http.get('/api/clusters/:id', ({ params }) => {
    const cluster = CLUSTER_DETAILS[params.id as string]
    return cluster ? HttpResponse.json(cluster) : notFound()
  }),
  http.get('/api/services/:id/tasks', ({ params, request }) => {
    const id = params.id as string
    if (!SERVICE_DETAILS[id]) return notFound()
    // Like the API: `problems` unless the caller asks for another filter.
    const filter = new URL(request.url).searchParams.get('filter') ?? 'problems'
    const serviceTasks = ALL_TASKS.filter((t) => t.service?.id === id)
    return HttpResponse.json({ ...tasksList(serviceTasks), items: filterTasks(serviceTasks, filter) })
  }),
  http.get('/api/services/:id/operations', ({ params, request }) => {
    const id = params.id as string
    if (!SERVICE_DETAILS[id]) return notFound()
    const includeEcs = new URL(request.url).searchParams.get('include_ecs') !== 'false'
    const items = OPERATIONS_FOR(id).filter((e) => includeEcs || e.source === 'harbormaster')
    return HttpResponse.json({ items, next: null, polled_at: FIXTURE_NOW })
  }),
  http.get('/api/services/:id', ({ params }) => {
    const service = SERVICE_DETAILS[params.id as string]
    return service ? HttpResponse.json(service) : notFound()
  }),
  http.get('/api/tasks', ({ request }) => {
    const url = new URL(request.url)
    return HttpResponse.json(tasksResponse(url.searchParams.get('filter'), url.searchParams.get('q')))
  }),
  http.get('/api/search', ({ request }) => HttpResponse.json({ items: search(new URL(request.url).searchParams.get('q') ?? '') })),
  http.get('/api/poller', () => HttpResponse.json(POLLER)),
  http.get('/api/users', () => HttpResponse.json(USERS)),
  http.get('/api/permissions', () => HttpResponse.json(PERMISSIONS)),
  http.get('/api/tags', () => HttpResponse.json(TAGS)),
  http.get('/api/tags/:id/impact', () => HttpResponse.json({ clusters: [], services: [], users: [] })),
  http.get('/api/tag-assignments', () => HttpResponse.json(ASSIGNMENTS)),
  http.get('/api/accounts', () => HttpResponse.json(ACCOUNTS)),
  http.get('/api/accounts/external-id', () => HttpResponse.json({ external_id: ACCOUNTS.items[0].external_id })),
  http.get('/api/audit', () => HttpResponse.json(AUDIT)),
  http.post('/api/accounts/test', async ({ request }) => {
    const body = (await request.json()) as { regions: string[] }
    return HttpResponse.json({ regions: body.regions.map((region) => ({ region, ok: true, clusters: 1, error: '' })) })
  }),
  http.post('/api/accounts', () => HttpResponse.json(ACCOUNTS.items[0], { status: 201 })),
  http.patch('/api/accounts/:id', () => new HttpResponse(null, { status: 204 })),
  http.delete('/api/accounts/:id', () => new HttpResponse(null, { status: 204 })),
  // observe (plan 03, task 16): overview metrics and alerts
  http.get('/api/overview/metrics', () => HttpResponse.json(OVERVIEW_METRICS)),
  http.get('/api/alerts', () => HttpResponse.json(ALERTS)),
  http.get('/api/clusters/metrics', () => HttpResponse.json(CLUSTERS_METRICS)),
  // observe (plan 03, task 19): service metrics and target health
  http.get('/api/services/:id/metrics', ({ params }) =>
    HttpResponse.json(params.id === SERVICE_PAYMENTS.id ? SERVICE_METRICS_PAYMENTS : SERVICE_METRICS_CATALOG_NO_INSIGHTS),
  ),
  http.get('/api/services/:id/target-health', () => HttpResponse.json(TARGET_HEALTH_EDGE)),
  // observe (plan 03, task 18): cluster page metrics
  http.get('/api/clusters/:id/metrics', () => HttpResponse.json(CLUSTER_METRICS_CORE)),
]

export const server = setupServer(...handlers)
