import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import {
  ACCOUNTS,
  ASSIGNMENTS,
  AUDIT,
  CLUSTERS,
  CLUSTER_DETAILS,
  ME_ADMIN,
  OPERATIONS_FOR,
  OVERVIEW,
  PERMISSIONS,
  POLLER,
  SERVICE_DETAILS,
  TAGS,
  USERS,
  ALL_TASKS,
  FIXTURE_NOW,
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
    ...ALL_TASKS.filter((t) => t.service).map((t) => ({ kind: 'task' as const, label: t.short_id, sub: `${t.service!.name} · ${t.cluster.name}`, href: `/services/${t.service!.id}/tasks` })),
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
  http.get('/api/services/:id/tasks', ({ params }) => {
    const id = params.id as string
    if (!SERVICE_DETAILS[id]) return notFound()
    return HttpResponse.json(tasksList(ALL_TASKS.filter((t) => t.service?.id === id)))
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
]

export const server = setupServer(...handlers)
