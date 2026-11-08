import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import {
  ACCOUNTS,
  ANALYTICS_CAPACITY,
  ANALYTICS_COST,
  ANALYTICS_DELIVERY,
  ANALYTICS_OPERATIONS,
  ANALYTICS_RELIABILITY,
  ASSIGNMENTS,
  AUDIT,
  CLUSTERS,
  CLUSTERS_METRICS,
  SERVICES_METRICS,
  CLUSTER_DETAILS,
  CLUSTER_METRICS_CORE,
  ME_ADMIN,
  OPERATIONS_FOR,
  OVERVIEW,
  PERMISSIONS,
  POLLER,
  LOGS_HISTORY_PAYMENTS,
  SERVICES_LIST,
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
  TASK_DEFINITION_EDITABLE,
  REVISIONS_PAYMENTS,
  IMAGE_TAGS_PAYMENTS,
  DESIRED_COUNT_PAYMENTS,
  SETTINGS,
  DEPLOY_SETTINGS_PAYMENTS,
  REVISION_DEFINITION_PAYMENTS_41,
  CHANGESET_APPLIED,
  AUTH_OPTIONS_OFF,
  SSO_CONFIGURED_OFF,
  SSO_TEST_OK,
  SSO_UNCONFIGURED,
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
    const tags = url.searchParams.getAll('tag').map(Number)
    const items = CLUSTERS.items.filter((c) => (!account || c.account_id === account) && (!region || c.region === region) && (tags.length === 0 || tags.includes(c.tag.id)))
    return HttpResponse.json({ ...CLUSTERS, items })
  }),
  http.get('/api/clusters/metrics', () => HttpResponse.json(CLUSTERS_METRICS)),
  // Registered before /api/services/:id so "metrics" is not read as an id. Mirrors the server: only the requested ids come back.
  http.get('/api/services/metrics', ({ request }) => {
    const ids = new Set(new URL(request.url).searchParams.getAll('id'))
    const items = Object.fromEntries(Object.entries(SERVICES_METRICS.items).filter(([id]) => ids.has(id)))
    return HttpResponse.json({ ...SERVICES_METRICS, items })
  }),
  http.get('/api/clusters/:id', ({ params }) => {
    const cluster = CLUSTER_DETAILS[params.id as string]
    return cluster ? HttpResponse.json(cluster) : notFound()
  }),
  // Services list: same account/region/tag narrowing as clusters.
  http.get('/api/services', ({ request }) => {
    const url = new URL(request.url)
    const account = url.searchParams.get('account')
    const region = url.searchParams.get('region')
    const tags = url.searchParams.getAll('tag').map(Number)
    const items = SERVICES_LIST.items.filter(
      (s) => (!account || s.account_id === account) && (!region || s.region === region) && (tags.length === 0 || tags.includes(s.tag.id)),
    )
    return HttpResponse.json({ ...SERVICES_LIST, items, total: items.length })
  }),
  // Log history; with `task` every line belongs to that task, as the server restricts reads to its streams.
  http.get('/api/services/:id/logs', ({ request }) => {
    const task = new URL(request.url).searchParams.get('task')
    return HttpResponse.json(task ? { ...LOGS_HISTORY_PAYMENTS, lines: LOGS_HISTORY_PAYMENTS.lines.map((line) => ({ ...line, task_id: task })) } : LOGS_HISTORY_PAYMENTS)
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
  // analytics: the fixture for the tab, with the requested range echoed into window.requested
  http.get('/api/analytics/:tab', ({ params, request }) => {
    const fixture = { delivery: ANALYTICS_DELIVERY, reliability: ANALYTICS_RELIABILITY, capacity: ANALYTICS_CAPACITY, operations: ANALYTICS_OPERATIONS, cost: ANALYTICS_COST }[
      params.tab as string
    ]
    if (!fixture) return notFound()
    const range = new URL(request.url).searchParams.get('range') ?? '30d'
    return HttpResponse.json({ ...fixture, window: { ...fixture.window, requested: range } })
  }),
  // observe (plan 03, task 19): service metrics and target health
  http.get('/api/services/:id/metrics', ({ params }) =>
    HttpResponse.json(params.id === SERVICE_PAYMENTS.id ? SERVICE_METRICS_PAYMENTS : SERVICE_METRICS_CATALOG_NO_INSIGHTS),
  ),
  http.get('/api/services/:id/target-health', () => HttpResponse.json(TARGET_HEALTH_EDGE)),
  // observe (plan 03, task 18): cluster page metrics
  http.get('/api/clusters/:id/metrics', () => HttpResponse.json(CLUSTER_METRICS_CORE)),
  // Change path reads (plan 02)
  http.get('/api/services/:id/task-definition', () => HttpResponse.json(TASK_DEFINITION_EDITABLE)),
  http.get('/api/services/:id/revisions', () => HttpResponse.json(REVISIONS_PAYMENTS)),
  http.get('/api/services/:id/image-tags', () => HttpResponse.json(IMAGE_TAGS_PAYMENTS)),
  http.get('/api/services/:id/desired-count', () => HttpResponse.json(DESIRED_COUNT_PAYMENTS)),
  // Deploy tab (docs/deploy_tab/PLAN.md section 2)
  http.get('/api/services/:id/deploy-settings', () => HttpResponse.json(DEPLOY_SETTINGS_PAYMENTS)),
  http.get('/api/services/:id/revisions/:revision', ({ params }) =>
    Number(params.revision) === REVISION_DEFINITION_PAYMENTS_41.revision ? HttpResponse.json(REVISION_DEFINITION_PAYMENTS_41) : notFound(),
  ),
  http.post('/api/services/:id/changeset', () => HttpResponse.json(CHANGESET_APPLIED)),
  http.get('/api/settings', () => HttpResponse.json(SETTINGS)),
  http.put('/api/settings', async ({ request }) => HttpResponse.json(await request.json())),
  // Single sign-on (plan 07). Admin routes answer with SSOConfigDTO; override per test to change the state.
  http.get('/api/auth/options', () => HttpResponse.json(AUTH_OPTIONS_OFF)),
  http.get('/api/auth/saml/metadata', () =>
    HttpResponse.text('<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata"/>', {
      headers: { 'Content-Type': 'application/samlmetadata+xml' },
    }),
  ),
  http.post('/api/auth/saml/link', () => HttpResponse.json({ redirect_url: 'https://idp.example/sso?x' })),
  http.get('/api/sso', () => HttpResponse.json(SSO_UNCONFIGURED)),
  http.put('/api/sso', () => HttpResponse.json(SSO_CONFIGURED_OFF)),
  http.post('/api/sso/metadata', () => HttpResponse.json(SSO_CONFIGURED_OFF)),
  http.put('/api/sso/enabled', async ({ request }) => {
    const body = (await request.json()) as { enabled: boolean }
    return HttpResponse.json({ ...SSO_CONFIGURED_OFF, enabled: body.enabled })
  }),
  http.post('/api/sso/key', () => HttpResponse.json(SSO_CONFIGURED_OFF)),
  http.get('/api/sso/tests/:id', () => HttpResponse.json(SSO_TEST_OK)),
  http.post('/api/users/:id/break-glass', () => new HttpResponse(null, { status: 204 })),
  http.put('/api/users/:id/sso-link', () => new HttpResponse(null, { status: 204 })),
  http.delete('/api/users/:id/sso-link', () => new HttpResponse(null, { status: 204 })),
  // Only alex has no password, so only alex gets a temporary one back.
  http.post('/api/users/:id/sso-unlink', ({ params }) => {
    const user = USERS.items.find((u) => String(u.id) === params.id)
    if (!user) return notFound()
    return HttpResponse.json(user.has_password ? {} : { temporary_password: 'tmp-pass-1234' })
  }),
]

export const server = setupServer(...handlers)
